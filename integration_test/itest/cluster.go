package itest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	sigsYaml "sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/dtest"
	telcharts "github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	TestUser = "telepresence-test-developer"
)

type Cluster interface {
	CapturePodLogs(ctx context.Context, app, container, ns string)
	CompatVersion() string
	Executable() string
	GeneralError() error
	GlobalEnv() map[string]string
	InstallTrafficManager(ctx context.Context, values map[string]string) error
	InstallTrafficManagerVersion(ctx context.Context, version string, values map[string]string) error
	IsCI() bool
	Registry() string
	SetGeneralError(error)
	Suffix() string
	TelepresenceVersion() string
	UninstallTrafficManager(ctx context.Context, managerNamespace string)
	PackageHelmChart(ctx context.Context) (string, error)
	GetValuesForHelm(ctx context.Context, values map[string]string, release bool) []string
	GetK8SCluster(ctx context.Context, context, managerNamespace string) (context.Context, *k8s.Cluster, error)
	TelepresenceHelmInstall(ctx context.Context, upgrade bool, args ...string) error
}

// The cluster is created once and then reused by all tests. It ensures that:
//
//   - executable and the images are built once
//   - a docker repository is available
//   - built images are pushed to the docker repository
//   - a cluster is available
type cluster struct {
	suffix           string
	isCI             bool
	prePushed        bool
	executable       string
	testVersion      string
	compatVersion    string
	registry         string
	kubeConfig       string
	generalError     error
	logCapturingPods sync.Map
	userdPProf       uint16
	rootdPProf       uint16
}

func WithCluster(ctx context.Context, f func(ctx context.Context)) {
	s := cluster{}
	s.suffix, s.isCI = dos.LookupEnv(ctx, "GITHUB_SHA")
	if s.isCI {
		// Use 7 characters of SHA to avoid busting k8s 60 character name limit
		if len(s.suffix) > 7 {
			s.suffix = s.suffix[:7]
		}
	} else {
		s.suffix = strconv.Itoa(os.Getpid())
	}
	s.testVersion, s.prePushed = dos.LookupEnv(ctx, "DEV_TELEPRESENCE_VERSION")
	if s.prePushed {
		dlog.Infof(ctx, "Using pre-pushed binary %s", s.testVersion)
	} else {
		s.testVersion = "v2.9.0-gotest.z" + s.suffix
		dlog.Infof(ctx, "Building temp binary %s", s.testVersion)
	}
	version.Version, version.Structured = version.Init(s.testVersion, "TELEPRESENCE_VERSION")
	s.compatVersion = dos.Getenv(ctx, "DEV_COMPAT_VERSION")

	t := getT(ctx)
	var agentImage Image
	agentImage.Name = "tel2"
	agentImage.Tag = s.testVersion[1:]
	if agentImageQN, ok := dos.LookupEnv(ctx, "DEV_AGENT_IMAGE"); ok {
		i := strings.LastIndexByte(agentImageQN, '/')
		if i >= 0 {
			agentImage.Registry = agentImageQN[:i]
			agentImageQN = agentImageQN[i+1:]
		}
		i = strings.IndexByte(agentImageQN, ':')
		require.Greater(t, i, 0)
		agentImage.Name = agentImageQN[:i]
		agentImage.Tag = agentImageQN[i+1:]
	}

	s.registry = dos.Getenv(ctx, "DTEST_REGISTRY")
	require.NoError(t, s.generalError)

	if pp := dos.Getenv(ctx, "DEV_USERD_PROFILING_PORT"); pp != "" {
		port, err := strconv.ParseUint(pp, 10, 16)
		require.NoError(t, err)
		s.userdPProf = uint16(port)
	}
	if pp := dos.Getenv(ctx, "DEV_ROOTD_PROFILING_PORT"); pp != "" {
		port, err := strconv.ParseUint(pp, 10, 16)
		require.NoError(t, err)
		s.rootdPProf = uint16(port)
	}
	ctx = withGlobalHarness(ctx, &s)
	if s.prePushed {
		s.executable = filepath.Join(GetModuleRoot(ctx), "build-output", "bin", "telepresence")
	}
	errs := make(chan error, 10)
	wg := &sync.WaitGroup{}
	wg.Add(3)
	go s.ensureExecutable(ctx, errs, wg)
	go s.ensureDockerImages(ctx, errs, wg)
	go s.ensureCluster(ctx, wg)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	if agentImage.Registry == "" {
		agentImage.Registry = s.registry
	}
	ctx = WithAgentImage(ctx, &agentImage)

	s.ensureQuit(ctx)
	_ = Run(ctx, "kubectl", "delete", "ns", "-l", "purpose=tp-cli-testing")
	defer s.tearDown(ctx)
	if !t.Failed() {
		f(s.withBasicConfig(ctx, t))
	}
}

func (s *cluster) tearDown(ctx context.Context) {
	s.ensureQuit(ctx)
	if s.kubeConfig != "" {
		ctx = WithWorkingDir(ctx, filepath.Join(GetOSSRoot(ctx), "integration_test"))
		_ = Run(ctx, "kubectl", "delete", "-f", filepath.Join("testdata", "k8s", "client_rbac.yaml"))
		_ = Run(ctx, "kubectl", "delete", "--wait=false", "ns", "-l", "purpose=tp-cli-testing")
	}
}

func (s *cluster) ensureQuit(ctx context.Context) {
	// Ensure that no telepresence is running when the tests start
	_, _, _ = Telepresence(ctx, "quit", "-s") //nolint:dogsled // don't care about any of the returns

	// Ensure that the daemon-socket is non-existent.
	_ = rmAsRoot(socket.RootDaemonPath(ctx))
}

func (s *cluster) ensureExecutable(ctx context.Context, errs chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	if s.executable != "" {
		return
	}

	ctx = WithModuleRoot(ctx)
	exe := "telepresence"
	env := map[string]string{
		"TELEPRESENCE_VERSION":  s.testVersion,
		"TELEPRESENCE_REGISTRY": s.registry,
	}
	if runtime.GOOS == "windows" {
		env["CGO_ENABLED"] = "0"
		exe += ".exe"
	}
	err := Run(WithEnv(ctx, env), "make", "build")
	if err != nil {
		errs <- err
		return
	}
	s.executable = filepath.Join(GetWorkingDir(ctx), "build-output", "bin", exe)
}

func (s *cluster) ensureDocker(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	s.registry = dtest.DockerRegistry(log.WithDiscardingLogger(ctx))
}

func (s *cluster) ensureDockerImages(ctx context.Context, errs chan<- error, wg *sync.WaitGroup) {
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
	if s.registry == "" {
		wgs.Add(1)
		go s.ensureDocker(ctx, wgs)
	}

	runMake := func(target string) {
		out, err := Command(WithEnv(WithModuleRoot(ctx), map[string]string{
			"TELEPRESENCE_VERSION":  s.testVersion,
			"TELEPRESENCE_REGISTRY": s.registry,
		}), makeExe, target).CombinedOutput()
		if err != nil {
			errs <- RunError(err, out)
		}
	}

	wgs.Add(2)
	go func() {
		defer wgs.Done()
		runMake("tel2-image")
	}()
	go func() {
		defer wgs.Done()
		runMake("client-image")
	}()
	wgs.Wait()

	//  Image built and a registry exists. Push the image
	runMake("push-images")
}

func (s *cluster) ensureCluster(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	if s.registry == "" {
		dwg := sync.WaitGroup{}
		dwg.Add(1)
		s.ensureDocker(ctx, &dwg)
		dwg.Wait()
	}
	t := getT(ctx)
	s.kubeConfig = dos.Getenv(ctx, "DTEST_KUBECONFIG")
	if s.kubeConfig == "" {
		s.kubeConfig = dtest.Kubeconfig(log.WithDiscardingLogger(ctx))
	}
	require.NoError(t, os.Chmod(s.kubeConfig, 0o600), "failed to chmod 0600 %q", s.kubeConfig)

	// Delete any lingering traffic-manager resources that aren't bound to specific namespaces.
	_ = Run(ctx, "kubectl", "delete", "mutatingwebhookconfiguration,role,rolebinding", "-l", "app=traffic-manager")
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

	config.Images.PrivateRegistry = s.Registry()
	if agentImage := GetAgentImage(c); agentImage != nil {
		config.Images.PrivateWebhookRegistry = agentImage.Registry
	} else {
		config.Images.PrivateWebhookRegistry = s.Registry()
	}

	config.Grpc.MaxReceiveSize, _ = resource.ParseQuantity("10Mi")
	config.Cloud.SystemaHost = "127.0.0.1"

	config.Intercept.UseFtp = true

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
		"KUBECONFIG": s.kubeConfig,
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

func (s *cluster) CompatVersion() string {
	return s.compatVersion
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

	// Use another logger to avoid errors due to logs arriving after the tests complete.
	ctx = dlog.WithLogger(ctx, dlog.WrapLogrus(logrus.StandardLogger()))
	dlog.Infof(ctx, "Capturing logs for pods %q", pods)
	for _, pod := range strings.Split(pods, " ") {
		if _, ok := s.logCapturingPods.LoadOrStore(pod, present); ok {
			continue
		}
		logFile, err := os.Create(
			filepath.Join(filelocation.AppUserLogDir(ctx), fmt.Sprintf("%s-%s.log", dtime.Now().Format("20060102T150405"), pod)))
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

func (s *cluster) PackageHelmChart(ctx context.Context) (string, error) {
	filename := filepath.Join(getT(ctx).TempDir(), "telepresence-chart.tgz")
	fh, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return "", err
	}
	if err := telcharts.WriteChart(telcharts.DirTypeTelepresence, fh, "telepresence", s.TelepresenceVersion()[1:]); err != nil {
		_ = fh.Close()
		return "", err
	}
	if err := fh.Close(); err != nil {
		return "", err
	}
	return filename, nil
}

func (s *cluster) GetValuesForHelm(ctx context.Context, values map[string]string, release bool) []string {
	nss := GetNamespaces(ctx)
	settings := []string{
		"--set", "logLevel=debug",
	}
	if len(nss.ManagedNamespaces) > 0 {
		settings = append(settings,
			"--set", fmt.Sprintf("clientRbac.namespaces=%s", nss.HelmString()),
			"--set", fmt.Sprintf("managerRbac.namespaces=%s", nss.HelmString()),
		)
	}
	agentImage := GetAgentImage(ctx)
	if agentImage != nil {
		settings = append(settings,
			"--set", fmt.Sprintf("agentInjector.agentImage.name=%s", agentImage.Name), // Prevent attempts to retrieve image from SystemA
			"--set", fmt.Sprintf("agentInjector.agentImage.tag=%s", agentImage.Tag),
		)
	}
	if sysA := GetSystemA(ctx); sysA != nil {
		settings = append(settings,
			"--set", fmt.Sprintf("systemaHost=%s", sysA.SystemaHost),
			"--set", fmt.Sprintf("systemaPort=%d", sysA.SystemaPort),
		)
	}
	if !release {
		settings = append(settings, "--set", fmt.Sprintf("image.registry=%s", s.Registry()))
		if agentImage != nil {
			settings = append(settings, "--set", fmt.Sprintf("agentInjector.agentImage.registry=%s", agentImage.Registry))
		}
	}

	for k, v := range values {
		settings = append(settings, "--set", k+"="+v)
	}
	return settings
}

func (s *cluster) InstallTrafficManager(ctx context.Context, values map[string]string) error {
	chartFilename, err := s.PackageHelmChart(ctx)
	if err != nil {
		return err
	}
	return s.installChart(ctx, false, chartFilename, values)
}

// InstallTrafficManagerVersion performs a helm install of a specific version of the traffic-manager using
// the helm registry at https://app.getambassador.io. It is assumed that the image to use for the traffic-manager
// can be pulled from the standard registry at docker.io/datawire, and that the traffic-manager image is
// configured using DEV_AGENT_IMAGE.
//
// The intent is to simulate connection to an older cluster from the current client.
func (s *cluster) InstallTrafficManagerVersion(ctx context.Context, version string, values map[string]string) error {
	chartFilename, err := s.pullHelmChart(ctx, version)
	if err != nil {
		return err
	}
	return s.installChart(ctx, true, chartFilename, values)
}

func (s *cluster) installChart(ctx context.Context, release bool, chartFilename string, values map[string]string) error {
	settings := s.GetValuesForHelm(ctx, values, release)

	ctx = WithWorkingDir(ctx, filepath.Join(GetOSSRoot(ctx), "integration_test"))
	nss := GetNamespaces(ctx)
	args := []string{"install", "-n", nss.Namespace, "--wait"}
	args = append(args, settings...)
	args = append(args, "traffic-manager", chartFilename)

	err := Run(ctx, "helm", args...)
	if err == nil {
		err = RolloutStatusWait(ctx, nss.Namespace, "deploy/traffic-manager")
		if err == nil {
			s.CapturePodLogs(ctx, "app=traffic-manager", "", nss.Namespace)
		}
	}
	return err
}

func (s *cluster) TelepresenceHelmInstall(ctx context.Context, upgrade bool, settings ...string) error {
	nss := GetNamespaces(ctx)
	subjectNames := []string{TestUser}
	subjects := make([]rbac.Subject, len(subjectNames))
	for i, s := range subjectNames {
		subjects[i] = rbac.Subject{
			Kind:      "ServiceAccount",
			Name:      s,
			Namespace: nss.Namespace,
		}
	}

	type xRbac struct {
		Create     bool           `json:"create"`
		Namespaced bool           `json:"namespaced"`
		Subjects   []rbac.Subject `json:"subjects,omitempty"`
		Namespaces []string       `json:"namespaces,omitempty"`
	}
	type xAgent struct {
		Image *Image `json:"image,omitempty"`
	}
	var agent *xAgent
	if agentImage := GetAgentImage(ctx); agentImage != nil {
		agent = &xAgent{Image: agentImage}
	}
	nsl := nss.UniqueList()
	vx := struct {
		SystemaHost string  `json:"systemaHost"`
		SystemaPort int     `json:"systemaPort"`
		LogLevel    string  `json:"logLevel"`
		Image       *Image  `json:"image,omitempty"`
		Agent       *xAgent `json:"agent,omitempty"`
		ClientRbac  xRbac   `json:"clientRbac"`
		ManagerRbac xRbac   `json:"managerRbac"`
	}{
		LogLevel: "debug",
		Image:    GetImage(ctx),
		Agent:    agent,
		ClientRbac: xRbac{
			Create:     true,
			Namespaced: len(nss.ManagedNamespaces) > 0,
			Subjects:   subjects,
			Namespaces: nsl,
		},
		ManagerRbac: xRbac{
			Create:     true,
			Namespaced: len(nss.ManagedNamespaces) > 0,
			Namespaces: nsl,
		},
	}
	if sysA := GetSystemA(ctx); sysA != nil {
		vx.SystemaHost = sysA.SystemaHost
		vx.SystemaPort = sysA.SystemaPort
	}
	ss, err := sigsYaml.Marshal(&vx)
	if err != nil {
		return err
	}
	valuesFile := filepath.Join(getT(ctx).TempDir(), "values.yaml")
	if err := os.WriteFile(valuesFile, ss, 0o644); err != nil {
		return err
	}

	verb := "install"
	if upgrade {
		verb = "upgrade"
		settings = append(settings, "--reuse-values")
	}
	args := []string{"helm", verb, "-n", nss.Namespace, "-f", valuesFile}
	args = append(args, settings...)

	if _, _, err = Telepresence(WithUser(ctx, "default"), args...); err != nil {
		return err
	}
	if err = RolloutStatusWait(ctx, nss.Namespace, "deploy/traffic-manager"); err != nil {
		return err
	}
	s.CapturePodLogs(ctx, "app=traffic-manager", "", nss.Namespace)
	return nil
}

func (s *cluster) pullHelmChart(ctx context.Context, version string) (string, error) {
	if err := Run(ctx, "helm", "repo", "add", "datawire", "https://app.getambassador.io"); err != nil {
		return "", err
	}
	if err := Run(ctx, "helm", "repo", "update"); err != nil {
		return "", err
	}
	dir := getT(ctx).TempDir()
	if err := Run(WithWorkingDir(ctx, dir), "helm", "pull", "datawire/telepresence", "--version", version); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("telepresence-%s.tgz", version)), nil
}

func (s *cluster) UninstallTrafficManager(ctx context.Context, managerNamespace string) {
	t := getT(ctx)
	ctx = WithUser(ctx, "default")
	TelepresenceOk(ctx, "helm", "uninstall", "--manager-namespace", managerNamespace)

	// Helm uninstall does deletions asynchronously, so let's wait until the deployment is gone
	assert.Eventually(t, func() bool { return len(RunningPods(ctx, "traffic-manager", managerNamespace)) == 0 },
		60*time.Second, 4*time.Second, "traffic-manager deployment was not removed")
	TelepresenceQuitOk(ctx)
}

func (s *cluster) GetK8SCluster(ctx context.Context, context, managerNamespace string) (context.Context, *k8s.Cluster, error) {
	_ = os.Setenv("KUBECONFIG", KubeConfig(ctx))
	flags := map[string]string{
		"namespace": managerNamespace,
	}
	if context != "" {
		flags["context"] = context
	}
	cfgAndFlags, err := client.NewKubeconfig(ctx, flags, managerNamespace)
	if err != nil {
		return ctx, nil, err
	}
	kc, err := k8s.NewCluster(ctx, cfgAndFlags, nil)
	if err != nil {
		return ctx, nil, err
	}
	return kc.WithK8sInterface(ctx), kc, nil
}

func KubeConfig(ctx context.Context) string {
	kubeConf, _ := LookupEnv(ctx, "KUBECONFIG")
	return kubeConf
}

// Command creates and returns a dexec.Cmd  initialized with the global environment
// from the cluster harness and any other environment that has been added using the
// WithEnv() function.
func Command(ctx context.Context, executable string, args ...string) *dexec.Cmd {
	getT(ctx).Helper()
	// Ensure that command has a timestamp and is somewhat readable
	dlog.Debug(ctx, "executing ", shellquote.ShellString(filepath.Base(executable), args))
	cmd := proc.CommandContext(ctx, executable, args...)
	cmd.DisableLogging = true
	env := GetGlobalHarness(ctx).GlobalEnv()
	maps.Merge(env, getEnv(ctx))
	for k, v := range env {
		env[k] = k + "=" + v
	}
	cmd.Env = maps.ToSortedSlice(env)
	cmd.Dir = GetWorkingDir(ctx)
	cmd.Stdin = dos.Stdin(ctx)
	return cmd
}

// TelepresenceOk executes the CLI command in a new process and requires the result to be OK.
func TelepresenceOk(ctx context.Context, args ...string) string {
	t := getT(ctx)
	t.Helper()
	stdout, stderr, err := Telepresence(ctx, args...)
	assert.NoError(t, err, "telepresence was unable to run, stdout %s", stdout)
	if err == nil {
		assert.Empty(t, stderr, "Expected stderr to be empty, but got: %s", stderr)
	}
	return stdout
}

// Telepresence executes the CLI command in a new process.
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
// and DEV_TELEPRESENCE_LOG_DIR from filelocation.AppUserLogDir.
func TelepresenceCmd(ctx context.Context, args ...string) *dexec.Cmd {
	t := getT(ctx)
	t.Helper()

	var stdout, stderr strings.Builder
	ctx = WithEnv(ctx, map[string]string{
		"DEV_TELEPRESENCE_CONFIG_DIR": filelocation.AppUserConfigDir(ctx),
		"DEV_TELEPRESENCE_LOG_DIR":    filelocation.AppUserLogDir(ctx),
	})

	gh := GetGlobalHarness(ctx)
	if len(args) > 0 && (args[0] == "connect" || args[0] == "config") {
		rest := args[1:]
		args = append(make([]string, 0, len(args)+3), args[0])
		if user := GetUser(ctx); user != "default" {
			args = append(args, "--as", "system:serviceaccount:"+user)
		}
		if gh.userdPProf > 0 {
			args = append(args, "--userd-profiling-port", strconv.Itoa(int(gh.userdPProf)))
		}
		if gh.rootdPProf > 0 {
			args = append(args, "--rootd-profiling-port", strconv.Itoa(int(gh.rootdPProf)))
		}
		args = append(args, rest...)
	}
	if UseDocker(ctx) {
		args = append([]string{"--docker"}, args...)
	}
	cmd := Command(ctx, gh.executable, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return cmd
}

// TelepresenceDisconnectOk tells telepresence to quit and asserts that the stdout contains the correct output.
func TelepresenceDisconnectOk(ctx context.Context) {
	AssertDisconnectOutput(ctx, TelepresenceOk(ctx, "quit"))
}

// AssertDisconnectOutput asserts that the stdout contains the correct output from a telepresence quit command.
func AssertDisconnectOutput(ctx context.Context, stdout string) {
	t := getT(ctx)
	assert.True(t, strings.Contains(stdout, "Telepresence Daemons disconnecting...done") ||
		strings.Contains(stdout, "Telepresence Daemons are already disconnected") ||
		strings.Contains(stdout, "Telepresence Daemons have already quit"))
	if t.Failed() {
		t.Logf("Disconnect output was %q", stdout)
	}
}

// TelepresenceQuitOk tells telepresence to quit and asserts that the stdout contains the correct output.
func TelepresenceQuitOk(ctx context.Context) {
	AssertQuitOutput(ctx, TelepresenceOk(ctx, "quit", "-s"))
}

// AssertQuitOutput asserts that the stdout contains the correct output from a telepresence quit command.
func AssertQuitOutput(ctx context.Context, stdout string) {
	t := getT(ctx)
	assert.True(t, strings.Contains(stdout, "Telepresence Daemons quitting...done") ||
		strings.Contains(stdout, "Telepresence Daemons have already quit"))
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
// namespace is an empty string.
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

// KubectlOut runs kubectl with the default context and the application namespace and returns its combined output.
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
	assert.NoError(getT(ctx), Kubectl(ctx, namespace, "delete", "--ignore-not-found", "--grace-period", "3", "svc,"+workload, name),
		"failed to delete service and %s %s", workload, name)
}

// ApplyApp calls kubectl apply -n <namespace> -f on the given app + .yaml found in testdata/k8s relative
// to the directory returned by GetWorkingDir.
func ApplyApp(ctx context.Context, name, namespace, workload string) {
	t := getT(ctx)
	t.Helper()
	manifest := filepath.Join("testdata", "k8s", name+".yaml")
	require.NoError(t, Kubectl(ctx, namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
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

// StartLocalHttpEchoServerWithAddress is like StartLocalHttpEchoServer but binds to a specific host instead of localhost.
func StartLocalHttpEchoServerWithHost(ctx context.Context, name string, host string) (int, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
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

// StartLocalHttpEchoServer starts a local http server that echoes a line with the given name and
// the current URL path. The port is returned together with function that cancels the server.
func StartLocalHttpEchoServer(ctx context.Context, name string) (int, context.CancelFunc) {
	return StartLocalHttpEchoServerWithHost(ctx, name, "localhost")
}

// PingInterceptedEchoServer assumes that a server has been created using StartLocalHttpEchoServer and
// that an intercept is active for the given svc and svcPort that will redirect to that local server.
func PingInterceptedEchoServer(ctx context.Context, svc, svcPort string) {
	expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
	require.Eventually(getT(ctx), func() bool {
		// condition
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", svc)
		if err != nil {
			dlog.Info(ctx, err)
			return false
		}
		if len(ips) != 1 {
			dlog.Infof(ctx, "Lookup for %s returned %v", svc, ips)
			return false
		}

		hc := http.Client{Timeout: 2 * time.Second}
		resp, err := hc.Get(fmt.Sprintf("http://%s", net.JoinHostPort(ips[0].String(), svcPort)))
		if err != nil {
			dlog.Info(ctx, err)
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			dlog.Info(ctx, err)
			return false
		}
		r := string(body)
		if r != expectedOutput {
			dlog.Infof(ctx, "body: %q != %q", r, expectedOutput)
			return false
		}
		return true
	},
		time.Minute,   // waitFor
		5*time.Second, // polling interval
		`body of %q equals %q`, "http://"+svc, expectedOutput,
	)
}

func WithConfig(c context.Context, modifierFunc func(config *client.Config)) context.Context {
	// Quit a running daemon. We're changing the directory where its config resides.
	TelepresenceQuitOk(c)

	t := getT(c)
	configCopy := *client.GetConfig(c)
	modifierFunc(&configCopy)
	configYaml, err := yaml.Marshal(&configCopy)
	require.NoError(t, err)
	configYamlStr := string(configYaml)
	configDir := t.TempDir()
	c = filelocation.WithAppUserConfigDir(c, configDir)
	c, err = client.SetConfig(c, configDir, configYamlStr)
	require.NoError(t, err)
	return c
}

func WithKubeConfigExtension(ctx context.Context, extProducer func(*api.Cluster) map[string]any) context.Context {
	kc := KubeConfig(ctx)
	t := getT(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	require.NoError(t, err, "unable to read %s", kc)
	cc := cfg.Contexts[cfg.CurrentContext]
	require.NotNil(t, cc, "unable to get current context from config")
	cluster := cfg.Clusters[cc.Cluster]
	require.NotNil(t, cluster, "unable to get current cluster from config")

	raw, err := json.Marshal(extProducer(cluster))
	require.NoError(t, err, "unable to json.Marshal extension map")
	cluster.Extensions = map[string]k8sruntime.Object{"telepresence.io": &k8sruntime.Unknown{Raw: raw}}

	context := *cc
	context.Cluster = "extra"
	cfg = &api.Config{
		Kind:           "Config",
		APIVersion:     "v1",
		Preferences:    api.Preferences{},
		Clusters:       map[string]*api.Cluster{"extra": cluster},
		Contexts:       map[string]*api.Context{"extra": &context},
		CurrentContext: "extra",
	}
	kubeconfigFileName := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*cfg, kubeconfigFileName), "unable to write modified kubeconfig")
	return WithEnv(ctx, map[string]string{"KUBECONFIG": strings.Join([]string{kc, kubeconfigFileName}, string([]byte{os.PathListSeparator}))})
}

// RunningPods return the names of running pods with app=<service name>. Running here means
// that at least one container is still running. I.e. the pod might well be terminating
// but still considered running.
func RunningPods(ctx context.Context, svc, ns string) []string {
	out, err := KubectlOut(ctx, ns, "get", "pods", "-o", "json",
		"--field-selector", "status.phase==Running",
		"-l", "app="+svc)
	if err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	var pm core.PodList
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&pm); err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	pods := make([]string, 0, len(pm.Items))
nextPod:
	for _, pod := range pm.Items {
		for _, cn := range pod.Status.ContainerStatuses {
			if r := cn.State.Running; r != nil && !r.StartedAt.IsZero() {
				// At least one container is still running.
				pods = append(pods, pod.Name)
				continue nextPod
			}
		}
	}
	dlog.Infof(ctx, "Running pods %v", pods)
	return pods
}
