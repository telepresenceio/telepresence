package itest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const TestUser = "telepresence-test-developer"
const TestUserAccount = "system:serviceaccount:default:" + TestUser

type Cluster interface {
	CapturePodLogs(ctx context.Context, app, container, ns string)
	Executable() string
	GeneralError() error
	GlobalEnv() map[string]string
	InstallTrafficManager(ctx context.Context, managerNamespace string, appNamespaces ...string) error
	IsCI() bool
	Registry() string
	SetGeneralError(error)
	Suffix() string
	TelepresenceVersion() string
	UninstallTrafficManager(ctx context.Context, managerNamespace string)
}

// The cluster is created once and then reused by all tests. It ensures that:
//
//  - executable and the images are built once
//  - a docker repository is available
//  - built images are pushed to the docker repository
//  - a cluster is available
type cluster struct {
	suffix           string
	isCI             bool
	prePushed        bool
	executable       string
	testVersion      string
	registry         string
	moduleRoot       string
	kubeConfig       string
	generalError     error
	logCapturingPods sync.Map
}

func WithCluster(ctx context.Context, f func(ctx context.Context)) {
	s := cluster{}
	s.suffix, s.isCI = os.LookupEnv("GITHUB_SHA")
	if s.isCI {
		// Use 7 characters of SHA to avoid busting k8s 60 character name limit
		if len(s.suffix) > 7 {
			s.suffix = s.suffix[:7]
		}
	} else {
		s.suffix = strconv.Itoa(os.Getpid())
	}
	s.testVersion, s.prePushed = os.LookupEnv("DEV_TELEPRESENCE_VERSION")
	if !s.prePushed {
		s.testVersion = "v2.4.5-gotest.z" + s.suffix
	}
	version.Version = s.testVersion

	s.registry = os.Getenv("DTEST_REGISTRY")
	if s.registry == "" {
		s.registry = "localhost:5000"
	}
	t := getT(ctx)
	require.NoError(t, s.generalError)

	ctx = withGlobalHarness(ctx, &s)
	mrCtx := WithModuleRoot(ctx)
	s.moduleRoot = GetWorkingDir(mrCtx)
	if s.prePushed {
		s.executable = filepath.Join(s.moduleRoot, "build-output", "bin", "telepresence")
	}
	errs := make(chan error, 10)
	wg := &sync.WaitGroup{}
	wg.Add(3)
	go s.ensureExecutable(mrCtx, errs, wg)
	go s.ensureDockerImage(mrCtx, errs, wg)
	go s.ensureCluster(mrCtx, wg)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	s.ensureQuitAndLoggedOut(ctx)
	_ = Run(ctx, "kubectl", "delete", "ns", "-l", "purpose=tp-cli-testing")
	defer s.tearDown(ctx)
	if !t.Failed() {
		f(WithUser(s.withBasicConfig(ctx, t), TestUser))
	}
}

func (s *cluster) tearDown(ctx context.Context) {
	s.ensureQuitAndLoggedOut(ctx)
	if s.kubeConfig != "" {
		_ = Run(ctx, "kubectl", "delete", "-f", filepath.Join(s.moduleRoot, "k8s", "client_rbac.yaml"))
	}
}

func (s *cluster) ensureQuitAndLoggedOut(ctx context.Context) {
	// Ensure that telepresence is not logged in
	_, _, _ = Telepresence(ctx, "logout") //nolint:dogsled // don't care about any of the returns

	// Ensure that no telepresence is running when the tests start
	_, _, _ = Telepresence(ctx, "quit", "-ur") //nolint:dogsled // don't care about any of the returns

	// Ensure that the daemon-socket is non-existent.
	_ = rmAsRoot(client.DaemonSocketName)
}

func (s *cluster) ensureExecutable(ctx context.Context, errs chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	if s.executable != "" {
		return
	}
	if err := Run(ctx, "go", "run", filepath.Join("build-aux", "package_embedded_chart", "main.go"), s.testVersion); err != nil {
		errs <- err
		return
	}
	defer func() {
		if err := Run(ctx, "git", "restore", filepath.Join("pkg", "install", "helm", "telepresence-chart.tgz")); err != nil {
			errs <- err
		}
	}()

	exe := "telepresence"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	err := Run(ctx, "go", "build", "-ldflags",
		fmt.Sprintf("-X=github.com/telepresenceio/telepresence/v2/pkg/version.Version=%s", s.testVersion),
		"-o", filepath.Join("build-output", "bin", exe), "./cmd/telepresence")
	if err != nil {
		errs <- err
		return
	}
	s.executable = filepath.Join(GetWorkingDir(ctx), "build-output", "bin", exe)
}

func (s *cluster) ensureDocker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	dtest.DockerRegistry(log.WithDiscardingLogger(ctx))
}

func (s *cluster) ensureDockerImage(ctx context.Context, errs chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	if s.prePushed || s.isCI {
		return
	}
	makeExe := "make"
	if runtime.GOOS == "windows" {
		makeExe = "winmake.bat"
	}

	// Initialize docker and build image simultaneously
	wgs := &sync.WaitGroup{}
	if s.registry == "localhost:5000" {
		wgs.Add(1)
		go s.ensureDocker(ctx, wgs)
	}

	runMake := func(target string) {
		out, err := Command(ctx, makeExe, target).CombinedOutput()
		if err != nil {
			errs <- RunError(err, out)
		}
	}

	wgs.Add(1)
	go func() {
		defer wgs.Done()
		runMake("tel2")
	}()
	wgs.Wait()

	//  Image built and a registry exists. Push the image
	runMake("push-image")
}

func (s *cluster) ensureCluster(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	if s.registry == "localhost:5000" {
		dwg := sync.WaitGroup{}
		dwg.Add(1)
		s.ensureDocker(ctx, &dwg)
		dwg.Wait()
	}
	t := getT(ctx)
	s.kubeConfig = dtest.Kubeconfig(log.WithDiscardingLogger(ctx))
	require.NoError(t, os.Chmod(s.kubeConfig, 0600), "failed to chmod 0600 %q", s.kubeConfig)

	// Delete any lingering traffic-manager resources that aren't bound to specific namespaces.
	_ = Run(ctx, "kubectl", "delete", "mutatingwebhookconfiguration,clusterrole,clusterrolebinding", "-l", "app=traffic-manager")

	err := Run(ctx, "kubectl", "apply", "-f", filepath.Join("k8s", "client_rbac.yaml"))
	require.NoError(t, err, "failed to create %s service account", TestUser)
}

// PodCreateTimeout will return a timeout suitable for operations that create pods.
// This is longer when running against clusters that scale up nodes on demand for new pods.
func PodCreateTimeout(c context.Context) time.Duration {
	switch GetProfile(c) {
	case GkeAutopilotProfile:
		return 5 * time.Minute
	case DefaultProfile:
		fallthrough
	default: // this really shouldn't be happening but hey
		return 180 * time.Second
	}
}

func (s *cluster) withBasicConfig(c context.Context, t *testing.T) context.Context {
	config := client.GetDefaultConfig()
	config.LogLevels.UserDaemon = logrus.DebugLevel
	config.LogLevels.RootDaemon = logrus.DebugLevel

	to := &config.Timeouts
	to.PrivateAgentInstall = PodCreateTimeout(c)
	to.PrivateApply = PodCreateTimeout(c)
	to.PrivateClusterConnect = 60 * time.Second
	to.PrivateEndpointDial = 10 * time.Second
	to.PrivateHelm = PodCreateTimeout(c)
	to.PrivateIntercept = 30 * time.Second
	to.PrivateProxyDial = 30 * time.Second
	to.PrivateRoundtripLatency = 5 * time.Second
	to.PrivateTrafficManagerAPI = 120 * time.Second
	to.PrivateTrafficManagerConnect = 180 * time.Second

	registry := s.Registry()
	config.Images.PrivateRegistry = registry
	config.Images.PrivateWebhookRegistry = registry

	config.Grpc.MaxReceiveSize, _ = resource.ParseQuantity("10Mi")
	config.Cloud.SystemaHost = "127.0.0.1"

	configYaml, err := yaml.Marshal(&config)
	require.NoError(t, err)
	configYamlStr := string(configYaml)

	configDir := t.TempDir()
	c = filelocation.WithAppUserConfigDir(c, configDir)
	c, err = client.SetConfig(c, configDir, configYamlStr)
	require.NoError(t, err)
	return c
}

func (s *cluster) GlobalEnv() map[string]string {
	globalEnv := map[string]string{
		"TELEPRESENCE_VERSION":      s.testVersion,
		"TELEPRESENCE_REGISTRY":     s.registry,
		"TELEPRESENCE_LOGIN_DOMAIN": "localhost",
		"KUBECONFIG":                s.kubeConfig,
	}
	yes := struct{}{}
	includeEnv := map[string]struct{}{
		"HOME":                      yes,
		"PATH":                      yes,
		"LOGNAME":                   yes,
		"USER":                      yes,
		"TMPDIR":                    yes,
		"MAKELEVEL":                 yes,
		"TELEPRESENCE_MAX_LOGFILES": yes,
	}
	if runtime.GOOS == "windows" {
		includeEnv["APPDATA"] = yes
		includeEnv["AppData"] = yes
		includeEnv["LOCALAPPDATA"] = yes
		includeEnv["LocalAppData"] = yes
		includeEnv["OS"] = yes
		includeEnv["TEMP"] = yes
		includeEnv["TMP"] = yes
		includeEnv["Path"] = yes
		includeEnv["PATHEXT"] = yes
		includeEnv["ProgramFiles"] = yes
		includeEnv["ProgramData"] = yes
		includeEnv["SystemDrive"] = yes
		includeEnv["USERPROFILE"] = yes
		includeEnv["USERNAME"] = yes
		includeEnv["windir"] = yes
	}
	for _, env := range os.Environ() {
		if eqIdx := strings.IndexByte(env, '='); eqIdx > 0 {
			key := env[:eqIdx]
			if _, ok := includeEnv[key]; ok {
				globalEnv[key] = env[eqIdx+1:]
			}
		}
	}
	return globalEnv
}

func (s *cluster) Executable() string {
	return s.executable
}

func (s *cluster) GeneralError() error {
	return s.generalError
}

func (s *cluster) IsCI() bool {
	return s.isCI
}

func (s *cluster) Registry() string {
	return s.registry
}

func (s *cluster) SetGeneralError(err error) {
	s.generalError = err
}

func (s *cluster) Suffix() string {
	return s.suffix
}

func (s *cluster) TelepresenceVersion() string {
	return s.testVersion
}

func (s *cluster) CapturePodLogs(ctx context.Context, app, container, ns string) {
	var pods string
	for i := 0; ; i++ {
		var err error
		pods, err = KubectlOut(ctx, ns, "get", "pods", "--field-selector", "status.phase=Running", "-l", app, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			dlog.Errorf(ctx, "failed to get %s pod in namespace %s: %v", app, ns, err)
			return
		}
		pods = strings.TrimSpace(pods)
		if pods != "" || i == 5 {
			break
		}
		dtime.SleepWithContext(ctx, 2*time.Second)
	}
	if pods == "" {
		dlog.Errorf(ctx, "found no %s pods in namespace %s", app, ns)
		return
	}

	// Let command die when the pod that it logs die
	ctx = dcontext.WithoutCancel(ctx)

	present := struct{}{}
	logDir, _ := filelocation.AppUserLogDir(ctx)

	// Use another logger to avoid errors due to logs arriving after the tests complete.
	ctx = dlog.WithLogger(ctx, dlog.WrapLogrus(logrus.StandardLogger()))
	for _, pod := range strings.Split(pods, " ") {
		if _, ok := s.logCapturingPods.LoadOrStore(pod, present); ok {
			continue
		}
		logFile, err := os.Create(filepath.Join(logDir, fmt.Sprintf("%s-%s.log", pod, dtime.Now().Format("20060102T150405"))))
		if err != nil {
			s.logCapturingPods.Delete(pod)
			dlog.Errorf(ctx, "unable to create pod logfile %s: %v", logFile.Name(), err)
			return
		}

		args := []string{"--namespace", ns, "logs", "-f", pod}
		if container != "" {
			args = append(args, "-c", container)
		}
		cmd := Command(ctx, "kubectl", args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		go func(pod string) {
			defer func() {
				_ = logFile.Close()
				s.logCapturingPods.Delete(pod)
			}()
			if err := cmd.Run(); err != nil {
				dlog.Errorf(ctx, "log capture failed: %v", err)
			}
		}(pod)
	}
}

func (s *cluster) InstallTrafficManager(ctx context.Context, managerNamespace string, appNamespaces ...string) error {
	helmValues := filepath.Join("integration_test", "testdata", "test-values.yaml")
	helmChart := filepath.Join("charts", "telepresence")
	err := Run(WithModuleRoot(ctx), "helm", "install", "traffic-manager",
		"-n", managerNamespace, helmChart,
		"--set", fmt.Sprintf("image.registry=%s", s.Registry()),
		"--set", fmt.Sprintf("image.tag=%s", s.TelepresenceVersion()[1:]),
		"--set", fmt.Sprintf("clientRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		"--set", fmt.Sprintf("managerRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		// We don't want the tests or telepresence to depend on an extension host resolving, so we set it to localhost.
		"--set", "systemaHost=127.0.0.1",
		"-f", helmValues,
		"--wait",
	)
	if err == nil {
		err = RolloutStatusWait(ctx, managerNamespace, "deploy/traffic-manager")
		if err == nil {
			s.CapturePodLogs(ctx, "app=traffic-manager", "", managerNamespace)
		}
	}
	return err
}

func (s *cluster) UninstallTrafficManager(ctx context.Context, managerNamespace string) {
	TelepresenceOk(ctx, "quit")
	t := getT(ctx)
	require.NoError(t, Run(ctx, "helm", "uninstall", "traffic-manager", "-n", managerNamespace))

	// Helm uninstall does deletions asynchronously, so let's wait until the deployment is gone
	assert.Eventually(t, func() bool {
		return Kubectl(ctx, managerNamespace, "get", "deploy", "traffic-manager") != nil
	}, 20*time.Second, 2*time.Second, "traffic-manager deployment was not removed")
}

func KubeConfig(ctx context.Context) string {
	kubeConf, _ := LookupEnv(ctx, "KUBECONFIG")
	return kubeConf
}

// Command creates and returns a dexec.Cmd  initialized with the global environment
// from the cluster harness and any other environment that has been added using the
// WithEnv() function
func Command(ctx context.Context, executable string, args ...string) *dexec.Cmd {
	getT(ctx).Helper()
	// Ensure that command has a timestamp and is somewhat readable
	dlog.Debug(ctx, "executing ", shellquote.ShellString(filepath.Base(executable), args))
	cmd := dexec.CommandContext(ctx, executable, args...)
	cmd.DisableLogging = true
	env := GetGlobalHarness(ctx).GlobalEnv()
	for k, v := range getEnv(ctx) {
		env[k] = v
	}
	keys := make([]string, len(env))
	i := 0
	for k := range env {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	// reuse the keys slice for complete key=value strings
	for i, k := range keys {
		keys[i] = k + "=" + env[k]
	}
	cmd.Env = keys
	cmd.Dir = GetWorkingDir(ctx)
	return cmd
}

// TelepresenceOk executes the CLI command in a new process and requires the result to be OK
func TelepresenceOk(ctx context.Context, args ...string) string {
	t := getT(ctx)
	t.Helper()
	stdout, stderr, err := Telepresence(ctx, args...)
	require.NoError(t, err, "telepresence was unable to run")
	require.Empty(t, stderr, "Expected stderr to be empty, but got: %s", stderr)
	return stdout
}

// Telepresence executes the CLI command in a new process
func Telepresence(ctx context.Context, args ...string) (string, string, error) {
	t := getT(ctx)
	t.Helper()
	cmd := TelepresenceCmd(ctx, args...)
	stdout := cmd.Stdout.(*strings.Builder)
	stderr := cmd.Stderr.(*strings.Builder)
	err := cmd.Run()
	errStr := strings.TrimSpace(stderr.String())
	if err != nil {
		err = RunError(err, []byte(errStr))
	}
	return strings.TrimSpace(stdout.String()), errStr, err
}

// TelepresenceCmd creates a dexec.Cmd using the Command function. Before the command is created,
// the environment is extended with DEV_TELEPRESENCE_CONFIG_DIR from filelocation.AppUserConfigDir
// and DEV_TELEPRESENCE_LOG_DIR from filelocation.AppUserLogDir
func TelepresenceCmd(ctx context.Context, args ...string) *dexec.Cmd {
	t := getT(ctx)
	t.Helper()
	configDir, err := filelocation.AppUserConfigDir(ctx)
	require.NoError(t, err)
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	ctx = WithEnv(ctx, map[string]string{
		"DEV_TELEPRESENCE_CONFIG_DIR": configDir,
		"DEV_TELEPRESENCE_LOG_DIR":    logDir,
	})

	if len(args) > 0 && args[0] == "connect" {
		if user := GetUser(ctx); user != "default" {
			na := make([]string, len(args)+2)
			na[0] = "--as"
			na[1] = "system:serviceaccount:default:" + user
			copy(na[2:], args)
			args = na
		}
	}
	cmd := Command(ctx, GetGlobalHarness(ctx).executable, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return cmd
}

// TelepresenceDisconnectOk tells telepresence to quit and asserts that the stdout contains the correct output
func TelepresenceDisconnectOk(ctx context.Context) {
	AssertDisconnectOutput(ctx, TelepresenceOk(ctx, "quit"))
}

// AssertDisconnectOutput asserts that the stdout contains the correct output from a telepresence quit command
func AssertDisconnectOutput(ctx context.Context, stdout string) {
	t := getT(ctx)
	assert.True(t, strings.Contains(stdout, "Telepresence Network disconnecting...done") ||
		strings.Contains(stdout, "Telepresence Network is already disconnected"))
	assert.True(t, strings.Contains(stdout, "Telepresence Traffic Manager disconnecting...done") ||
		strings.Contains(stdout, "Telepresence Traffic Manager is already disconnected"))
	if t.Failed() {
		t.Logf("Disconnect output was %q", stdout)
	}
}

// TelepresenceQuitOk tells telepresence to quit and asserts that the stdout contains the correct output
func TelepresenceQuitOk(ctx context.Context) {
	AssertQuitOutput(ctx, TelepresenceOk(ctx, "quit", "-ur"))
}

// AssertQuitOutput asserts that the stdout contains the correct output from a telepresence quit command
func AssertQuitOutput(ctx context.Context, stdout string) {
	t := getT(ctx)
	assert.True(t, strings.Contains(stdout, "Telepresence Network quitting...done") ||
		strings.Contains(stdout, "Telepresence Network had already quit"))
	assert.True(t, strings.Contains(stdout, "Telepresence Traffic Manager quitting...done") ||
		strings.Contains(stdout, "Telepresence Traffic Manager had already quit"))
	if t.Failed() {
		t.Logf("Quit output was %q", stdout)
	}
}

// RunError checks if the given err is a *exit.ExitError, and if so, extracts
// Stderr and the ExitCode from it.
func RunError(err error, out []byte) error {
	if ee, ok := err.(*dexec.ExitError); ok {
		switch {
		case len(ee.Stderr) > 0:
			err = fmt.Errorf("%s, exit code %d", string(ee.Stderr), ee.ExitCode())
		case utf8.ValidString(string(out)):
			err = fmt.Errorf("%s, exit code %d", string(out), ee.ExitCode())
		default:
			err = fmt.Errorf("exit code %d", ee.ExitCode())
		}
	}
	return err
}

// Run runs the given command and arguments and returns an error if the command failed.
func Run(ctx context.Context, exe string, args ...string) error {
	getT(ctx).Helper()
	out, err := Command(ctx, exe, args...).CombinedOutput()
	if err != nil {
		return RunError(err, out)
	}
	return nil
}

// Output runs the given command and arguments and returns its combined output and an error if the command failed.
func Output(ctx context.Context, exe string, args ...string) (string, error) {
	getT(ctx).Helper()
	out, err := Command(ctx, exe, args...).CombinedOutput()
	if err != nil {
		return string(out), RunError(err, out)
	}
	return string(out), nil
}

// Kubectl runs kubectl with the default context and the given namespace, or in the default namespace if the given
// namespace is an empty string
func Kubectl(ctx context.Context, namespace string, args ...string) error {
	getT(ctx).Helper()
	var ks []string
	if namespace != "" {
		ks = append(make([]string, 0, len(args)+2), "--namespace", namespace)
		ks = append(ks, args...)
	} else {
		ks = args
	}
	return Run(ctx, "kubectl", ks...)
}

// KubectlOut runs kubectl with the default context and the application namespace and returns its combined output
func KubectlOut(ctx context.Context, namespace string, args ...string) (string, error) {
	getT(ctx).Helper()
	var ks []string
	if namespace != "" {
		ks = append(make([]string, 0, len(args)+2), "--namespace", namespace)
		ks = append(ks, args...)
	} else {
		ks = args
	}
	return Output(ctx, "kubectl", ks...)
}

func ApplyEchoService(ctx context.Context, name, namespace string, port int) {
	ApplyService(ctx, name, namespace, "jmalloc/echo-server:0.1.0", port, 8080)
}

func ApplyService(ctx context.Context, name, namespace, image string, port, targetPort int) {
	t := getT(ctx)
	t.Helper()
	require.NoError(t, Kubectl(ctx, namespace, "create", "deploy", name, "--image", image), "failed to create deployment %s", name)
	require.NoError(t, Kubectl(ctx, namespace, "expose", "deploy", name, "--port", strconv.Itoa(port), "--target-port", strconv.Itoa(targetPort)),
		"failed to expose deployment %s", name)
	require.NoError(t, Kubectl(ctx, namespace, "rollout", "status", "-w", "deployment/"+name), "failed to deploy %s", name)
}

func DeleteSvcAndWorkload(ctx context.Context, workload, name, namespace string) {
	require.NoError(getT(ctx), Kubectl(ctx, namespace, "delete", "--ignore-not-found", "--grace-period", "3", "svc,"+workload, name),
		"failed to delete service and %s %s", workload, name)
}

func ApplyApp(ctx context.Context, name, namespace, workload string) {
	t := getT(ctx)
	t.Helper()
	manifest := fmt.Sprintf("k8s/%s.yaml", name)
	require.NoError(t, Kubectl(WithModuleRoot(ctx), namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
	require.NoError(t, RolloutStatusWait(ctx, namespace, workload))
}

func RolloutStatusWait(ctx context.Context, namespace, workload string) error {
	ctx, cancel := context.WithTimeout(ctx, PodCreateTimeout(ctx))
	defer cancel()
	switch {
	case strings.HasPrefix(workload, "pod/"):
		return Kubectl(ctx, namespace, "wait", workload, "--for", "condition=ready")
	case strings.HasPrefix(workload, "replicaset/"), strings.HasPrefix(workload, "statefulset/"):
		for {
			status := struct {
				ReadyReplicas int `json:"readyReplicas,omitempty"`
				Replicas      int `json:"replicas,omitempty"`
			}{}
			stdout, err := KubectlOut(ctx, namespace, "get", workload, "-o", "jsonpath={..status}")
			if err != nil {
				return err
			}
			if err = json.Unmarshal([]byte(stdout), &status); err != nil {
				return err
			}
			if status.ReadyReplicas == status.Replicas {
				return nil
			}
			dtime.SleepWithContext(ctx, 3*time.Second)
		}
	}
	return Kubectl(ctx, namespace, "rollout", "status", "-w", workload)
}

func CreateNamespaces(ctx context.Context, namespaces ...string) {
	t := getT(ctx)
	t.Helper()
	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	for _, ns := range namespaces {
		go func(ns string) {
			defer wg.Done()
			assert.NoError(t, Kubectl(ctx, "", "create", "namespace", ns), "failed to create namespace %q", ns)
			assert.NoError(t, Kubectl(ctx, "", "label", "namespace", ns, "purpose="+purposeLabel, fmt.Sprintf("app.kubernetes.io/name=%s", ns)))
		}(ns)
	}
	wg.Wait()
}

func DeleteNamespaces(ctx context.Context, namespaces ...string) {
	t := getT(ctx)
	t.Helper()
	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	for _, ns := range namespaces {
		go func(ns string) {
			defer wg.Done()
			assert.NoError(t, Kubectl(ctx, "", "delete", "namespace", "--wait=false", ns))
		}(ns)
	}
	wg.Wait()
}

// StartLocalHttpEchoServer starts a local http server that echoes a line with the given name and
// the current URL path. The port is returned together with function that cancels the server.
func StartLocalHttpEchoServer(ctx context.Context, name string) (int, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", "localhost:0")
	require.NoError(getT(ctx), err, "failed to listen on localhost")
	go func() {
		sc := &dhttp.ServerConfig{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "%s from intercept at %s", name, r.URL.Path)
			}),
		}
		_ = sc.Serve(ctx, l)
	}()
	return l.Addr().(*net.TCPAddr).Port, cancel
}

func WithConfig(c context.Context, addConfig *client.Config) context.Context {
	if addConfig != nil {
		t := getT(c)
		origConfig := client.GetConfig(c)
		config := *origConfig // copy
		config.Merge(addConfig)
		configYaml, err := yaml.Marshal(&config)
		require.NoError(t, err)
		configYamlStr := string(configYaml)

		configDir := t.TempDir()
		c = filelocation.WithAppUserConfigDir(c, configDir)
		c, err = client.SetConfig(c, configDir, configYamlStr)
		require.NoError(t, err)
	}
	return c
}

func WithKubeConfigExtension(ctx context.Context, extProducer func(*api.Cluster) map[string]interface{}) context.Context {
	kc := KubeConfig(ctx)
	t := getT(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	require.NoError(t, err, "unable to read %s", kc)
	require.NoError(t, err, api.MinifyConfig(cfg), "unable to minify config")
	var cluster *api.Cluster
	for _, c := range cfg.Clusters {
		cluster = c
		break
	}
	require.NotNil(t, cluster, "unable to get cluster from config")

	raw, err := json.Marshal(extProducer(cluster))
	require.NoError(t, err, "unable to json.Marshal extension map")
	cluster.Extensions = map[string]k8sruntime.Object{"telepresence.io": &k8sruntime.Unknown{Raw: raw}}

	kubeconfigFileName := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*cfg, kubeconfigFileName), "unable to write modified kubeconfig")
	return WithEnv(ctx, map[string]string{"KUBECONFIG": kubeconfigFileName})
}
