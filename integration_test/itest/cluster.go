package itest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-json-experiment/json"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	TestUser = "telepresence-test-developer"
)

type Cluster interface {
	CapturePodLogs(ctx context.Context, app, container, ns string) string
	CompatVersion() string
	Executable() (string, error)
	GeneralError() error
	GlobalEnv(context.Context) dos.MapEnv
	AgentVersion(context.Context) string
	Initialize(context.Context) context.Context
	InstallTrafficManager(ctx context.Context, values map[string]any) error
	InstallTrafficManagerVersion(ctx context.Context, version string, values map[string]any) error
	IsCI() bool
	IsIPv6() bool
	LargeFileTestDisabled() bool
	Registry() string
	SetGeneralError(error)
	Suffix() string
	TelepresenceVersion() string
	UninstallTrafficManager(ctx context.Context, managerNamespace string, args ...string)
	PackageHelmChart(ctx context.Context) (string, error)
	GetValuesForHelm(ctx context.Context, values map[string]any, release bool) []string
	GetSetArgsForHelm(ctx context.Context, values map[string]any, release bool) []string
	GetK8SCluster(ctx context.Context, context, managerNamespace string) (context.Context, *k8s.Cluster, error)
	TelepresenceHelmInstallOK(ctx context.Context, upgrade bool, args ...string) string
	TelepresenceHelmInstall(ctx context.Context, upgrade bool, args ...string) (string, error)
	UserdPProf() uint16
	RootdPProf() uint16
}

// The cluster is created once and then reused by all tests. It ensures that:
//
//   - executable and the images are built once
//   - a docker repository is available
//   - built images are pushed to the docker repository
//   - a cluster is available
type cluster struct {
	suffix                string
	isCI                  bool
	prePushed             bool
	ipv6                  bool
	executable            string
	testVersion           string
	compatVersion         string
	registry              string
	kubeConfig            string
	generalError          error
	logCapturingPods      sync.Map
	userdPProf            uint16
	rootdPProf            uint16
	self                  Cluster
	largeFileTestDisabled bool
}

//nolint:gochecknoglobals // extension point
var ExtendClusterFunc = func(c Cluster) Cluster {
	return c
}

func WithCluster(ctx context.Context, f func(ctx context.Context)) {
	s := cluster{}
	s.self = &s
	ec := ExtendClusterFunc(&s)
	ctx = withGlobalHarness(ctx, ec)
	ctx = ec.Initialize(ctx)
	defer s.tearDown(ctx)
	t := getT(ctx)
	if !t.Failed() {
		f(s.withBasicConfig(ctx, t))
	}
}

func (s *cluster) SetSelf(self Cluster) {
	s.self = self
}

func (s *cluster) imagesFromEnv(ctx context.Context) context.Context {
	v := s.self.TelepresenceVersion()[1:]
	r := s.self.Registry()
	if img := ImageFromEnv(ctx, "DEV_MANAGER_IMAGE", v, r); img != nil {
		ctx = WithImage(ctx, img)
	}
	if img := ImageFromEnv(ctx, "DEV_CLIENT_IMAGE", v, r); img != nil {
		ctx = WithClientImage(ctx, img)
	}
	if img := ImageFromEnv(ctx, "DEV_AGENT_IMAGE", s.self.AgentVersion(ctx), r); img != nil {
		ctx = WithAgentImage(ctx, img)
	}
	return ctx
}

func (s *cluster) AgentVersion(ctx context.Context) string {
	return s.self.TelepresenceVersion()[1:]
}

func (s *cluster) Initialize(ctx context.Context) context.Context {
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
		s.testVersion = "v2.14.0-gotest.z" + s.suffix
		dlog.Infof(ctx, "Building temp binary %s", s.testVersion)
	}
	version.Version, version.Structured = version.Init(s.testVersion, "TELEPRESENCE_VERSION")
	s.compatVersion = dos.Getenv(ctx, "DEV_COMPAT_VERSION")

	t := getT(ctx)
	s.registry = dos.Getenv(ctx, "DTEST_REGISTRY")
	require.NoError(t, s.generalError)
	ctx = s.imagesFromEnv(ctx)

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
	if s.prePushed {
		exe := "telepresence"
		if runtime.GOOS == "windows" {
			exe = "telepresence.exe"
		}
		s.executable = filepath.Join(BuildOutput(ctx), "bin", exe)
	}
	s.largeFileTestDisabled, _ = strconv.ParseBool(dos.Getenv(ctx, "LARGE_FILE_TEST_DISABLED"))
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

	if ipv6, err := strconv.ParseBool("DEV_IPV6_CLUSTER"); err == nil {
		s.ipv6 = ipv6
	} else {
		output, err := Output(ctx, "kubectl", "--namespace", "kube-system", "get", "svc", "kube-dns", "-o", "jsonpath={.spec.clusterIP}")
		if err == nil {
			ip, err := netip.ParseAddr(strings.TrimSpace(output))
			assert.NoError(t, err)
			if ip.Is6() {
				dlog.Info(ctx, "Using IPv6 because the kube-dns.kube-system has an IPv6 IP")
				s.ipv6 = true
			}
		}
	}

	s.ensureQuit(ctx)
	_ = Run(ctx, "kubectl", "delete", "ns", "-l", "purpose=tp-cli-testing")
	return ctx
}

func (s *cluster) tearDown(ctx context.Context) {
	s.ensureQuit(ctx)
	if s.kubeConfig != "" {
		ctx = WithWorkingDir(ctx, GetOSSRoot(ctx))
		_ = Run(ctx, "kubectl", "delete", "-f", filepath.Join("testdata", "k8s", "client_rbac.yaml"))
		_ = Run(ctx, "kubectl", "delete", "--wait=false", "ns", "-l", "purpose=tp-cli-testing")
		_ = Run(ctx, "kubectl", "delete", "--wait=false", "pv", "-l", "purpose=tp-cli-testing")
		_ = Run(ctx, "kubectl", "delete", "--wait=false", "storageclass", "-l", "purpose=tp-cli-testing")
	}
}

func (s *cluster) ensureQuit(ctx context.Context) {
	// Ensure that no telepresence is running when the tests start
	_, _, _ = Telepresence(ctx, "quit", "-s") //nolint:dogsled // don't care about any of the returns

	// Ensure that the daemon-socket is non-existent.
	_ = rmAsRoot(ctx, socket.RootDaemonPath(ctx))
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
	s.executable = filepath.Join(BuildOutput(ctx), "bin", exe)
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
	config := client.GetDefaultConfigFunc()
	config.LogLevels().UserDaemon = logrus.DebugLevel
	config.LogLevels().RootDaemon = logrus.DebugLevel

	to := config.Timeouts()
	to.PrivateClusterConnect = 60 * time.Second
	to.PrivateEndpointDial = 10 * time.Second
	to.PrivateHelm = PodCreateTimeout(c)
	to.PrivateIntercept = 30 * time.Second
	to.PrivateProxyDial = 30 * time.Second
	to.PrivateRoundtripLatency = 5 * time.Second
	to.PrivateTrafficManagerAPI = 45 * time.Second
	to.PrivateTrafficManagerConnect = 30 * time.Second
	to.PrivateConnectivityCheck = 0

	images := config.Images()
	images.PrivateRegistry = s.self.Registry()
	if agentImage := GetAgentImage(c); agentImage != nil {
		images.PrivateAgentImage = agentImage.FQName()
		images.PrivateWebhookRegistry = agentImage.Registry
	}
	if clientImage := GetClientImage(c); clientImage != nil {
		images.PrivateClientImage = clientImage.FQName()
	}

	config.Grpc().MaxReceiveSizeV, _ = resource.ParseQuantity("10Mi")
	config.Intercept().UseFtp = true
	config.Routing().RecursionBlockDuration = 2 * time.Millisecond

	configYaml, err := config.MarshalYAML()
	require.NoError(t, err)
	configYamlStr := string(configYaml)

	configDir := t.TempDir()
	c = filelocation.WithAppUserConfigDir(c, configDir)
	c, err = SetConfig(c, configDir, configYamlStr)
	require.NoError(t, err)
	return c
}

func (s *cluster) GlobalEnv(ctx context.Context) dos.MapEnv {
	globalEnv := dos.MapEnv{
		"KUBECONFIG": s.kubeConfig,
	}
	yes := struct{}{}
	includeEnv := map[string]struct{}{
		"SCOUT_DISABLE":             yes,
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
	for _, env := range dos.Environ(ctx) {
		if eqIdx := strings.IndexByte(env, '='); eqIdx > 0 {
			key := env[:eqIdx]
			if _, ok := includeEnv[key]; ok {
				globalEnv[key] = env[eqIdx+1:]
			}
		}
	}
	return globalEnv
}

func (s *cluster) Executable() (string, error) {
	return s.executable, nil
}

func (s *cluster) GeneralError() error {
	return s.generalError
}

func (s *cluster) IsCI() bool {
	return s.isCI
}

func (s *cluster) IsIPv6() bool {
	return s.ipv6
}

func (s *cluster) LargeFileTestDisabled() bool {
	return s.largeFileTestDisabled
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

func (s *cluster) UserdPProf() uint16 {
	return s.userdPProf
}

func (s *cluster) RootdPProf() uint16 {
	return s.rootdPProf
}

func (s *cluster) CapturePodLogs(ctx context.Context, app, container, ns string) string {
	var pods []string
	for i := 0; ; i++ {
		runningPods := RunningPodNames(ctx, app, ns)
		if len(runningPods) > 0 {
			if container == "" {
				pods = runningPods
			} else {
				for _, pod := range runningPods {
					cns, err := KubectlOut(ctx, ns, "get", "pods", pod, "-o", "jsonpath={.spec.containers[*].name}")
					if err == nil && slice.Contains(strings.Split(cns, " "), container) {
						pods = append(pods, pod)
					}
				}
			}
		}
		if len(pods) > 0 || i == 5 {
			break
		}
		dtime.SleepWithContext(ctx, 2*time.Second)
	}

	if len(pods) == 0 {
		if container == "" {
			dlog.Errorf(ctx, "found no %s pods in namespace %s", app, ns)
		} else {
			dlog.Errorf(ctx, "found no %s pods in namespace %s with a %s container", app, ns, container)
		}
		return ""
	}
	present := struct{}{}

	var pod string
	for i, key := range pods {
		if container != "" {
			key += "/" + container
		}
		if _, ok := s.logCapturingPods.LoadOrStore(key, present); !ok {
			pod = pods[i]
			break
		}
	}
	if pod == "" {
		return "" // All pods already captured
	}

	// Use another logger to avoid errors due to logs arriving after the tests complete.
	ctx = dlog.WithLogger(ctx, dlog.WrapLogrus(logrus.StandardLogger()))
	logFile, err := os.Create(
		filepath.Join(filelocation.AppUserLogDir(ctx), fmt.Sprintf("%s-%s.log", dtime.Now().Format("20060102T150405"), pod)))
	if err != nil {
		s.logCapturingPods.Delete(pod)
		dlog.Errorf(ctx, "unable to create pod logfile %s: %v", logFile.Name(), err)
		return ""
	}

	args := []string{"--namespace", ns, "logs", "-f", pod}
	if container != "" {
		args = append(args, "-c", container)
	}
	// Let command die when the pod that it logs die
	cmd := Command(context.WithoutCancel(ctx), "kubectl", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	ready := make(chan string, 1)
	go func() {
		defer func() {
			_ = logFile.Close()
			s.logCapturingPods.Delete(pod)
		}()
		err := cmd.Start()
		if err == nil {
			if container == "" {
				dlog.Infof(ctx, "Capturing logs for pod %s", pod)
			} else {
				dlog.Infof(ctx, "Capturing logs for pod %s, container %s", pod, container)
			}
			ready <- logFile.Name()
			close(ready)
			err = cmd.Wait()
		}
		if err != nil {
			if container == "" {
				dlog.Errorf(ctx, "log capture for pod %s failed: %v", pod, err)
			} else {
				dlog.Errorf(ctx, "log capture for pod %s, container %s failed: %v", pod, container, err)
			}
			select {
			case <-ready:
			default:
				close(ready)
			}
		}
	}()
	select {
	case <-ctx.Done():
		dlog.Infof(ctx, "log capture for pod %s interrupted prior to start", pod)
		return ""
	case file := <-ready:
		return file
	}
}

func (s *cluster) GetK8SCluster(ctx context.Context, context, managerNamespace string) (context.Context, *k8s.Cluster, error) {
	_ = os.Setenv("KUBECONFIG", KubeConfig(ctx))
	flags := map[string]string{
		"namespace": managerNamespace,
	}
	if context != "" {
		flags["context"] = context
	}
	ctx, cfgAndFlags, err := client.NewKubeconfig(ctx, flags, managerNamespace)
	if err != nil {
		return ctx, nil, err
	}

	ctx, kc, err := k8s.NewCluster(ctx, cfgAndFlags, nil)
	if err != nil {
		return ctx, nil, err
	}
	return kc.WithJoinedClientSetInterface(ctx), kc, nil
}

func KubeConfig(ctx context.Context) string {
	kubeConf, _ := LookupEnv(ctx, "KUBECONFIG")
	return kubeConf
}

const sensitivePrefix = "--$sensitive$--"

// WrapSensitive wraps an argument sent to Command so that it doesn't get logged verbatim. This can
// be used for commands like "telepresence login --apikey NNNN" where the NNN shouldn't be visible
// in the logs. If NNN Is wrapped using this function, it will appear as "***" in the logs.
func WrapSensitive(s string) string {
	return sensitivePrefix + s
}

// Command creates and returns a dexec.Cmd  initialized with the global environment
// from the cluster harness and any other environment that has been added using the
// WithEnv() function.
func Command(ctx context.Context, executable string, args ...string) *dexec.Cmd {
	getT(ctx).Helper()
	// Ensure that command has a timestamp and is somewhat readable
	dbgArgs := args
	copied := false
	for i, a := range args {
		if strings.HasPrefix(a, sensitivePrefix) {
			if !copied {
				dbgArgs = make([]string, len(args))
				copy(dbgArgs, args)
				args = make([]string, len(args))
				copy(args, dbgArgs)
				copied = true
			}
			dbgArgs[i] = "***"
			args[i] = strings.TrimPrefix(a, sensitivePrefix)
		}
	}
	dlog.Debug(ctx, "executing ", shellquote.ShellString(filepath.Base(executable), dbgArgs))
	cmd := proc.CommandContext(ctx, executable, args...)
	cmd.DisableLogging = true
	cmd.Env = EnvironMap(ctx).Environ()
	cmd.Dir = GetWorkingDir(ctx)
	cmd.Stdin = dos.Stdin(ctx)
	return cmd
}

func EnvironMap(ctx context.Context) dos.MapEnv {
	env := GetGlobalHarness(ctx).GlobalEnv(ctx)
	maps.Merge(env, getEnv(ctx))
	return env
}

// TelepresenceOk executes the CLI command in a new process and requires the result to be OK.
func TelepresenceOk(ctx context.Context, args ...string) string {
	t := getT(ctx)
	t.Helper()
	stdout, stderr, err := Telepresence(ctx, args...)
	require.NoError(t, err, "telepresence was unable to run, stdout %s", stdout)
	if err == nil {
		if (strings.HasPrefix(stderr, "Warning:") || strings.Contains(stderr, "has been deprecated")) && !strings.ContainsRune(stderr, '\n') {
			// Accept warnings, but log them.
			dlog.Warn(ctx, stderr)
		} else {
			assert.Empty(t, stderr, "Expected stderr to be empty, but got: %s", stderr)
		}
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
	if len(args) > 0 && (args[0] == "connect") {
		rest := args[1:]
		args = append(make([]string, 0, len(args)+3), args[0])
		if user := GetUser(ctx); user != "default" {
			args = append(args, "--as", "system:serviceaccount:"+user)
		}
		if gh.UserdPProf() > 0 {
			args = append(args, "--userd-profiling-port", strconv.Itoa(int(gh.UserdPProf())))
		}
		if gh.RootdPProf() > 0 {
			args = append(args, "--rootd-profiling-port", strconv.Itoa(int(gh.RootdPProf())))
		}
		args = append(args, rest...)
	}
	exe, _ := gh.Executable()
	cmd := Command(ctx, exe, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return cmd
}

// TelepresenceDisconnectOk tells telepresence to quit and asserts that the stdout contains the correct output.
func TelepresenceDisconnectOk(ctx context.Context, args ...string) {
	AssertDisconnectOutput(ctx, TelepresenceOk(ctx, append([]string{"quit"}, args...)...))
}

// AssertDisconnectOutput asserts that the stdout contains the correct output from a telepresence quit command.
func AssertDisconnectOutput(ctx context.Context, stdout string) {
	t := getT(ctx)
	assert.True(t, strings.Contains(stdout, "Disconnected") || strings.Contains(stdout, "Not connected"))
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
	cmd := Command(ctx, exe, args...)
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), RunError(err, stderr.Bytes())
	}
	return string(out), nil
}

// Kubectl runs kubectl with the default context and the given namespace, or in the default namespace if the given
// namespace is an empty string.
func Kubectl(ctx context.Context, namespace string, args ...string) error {
	return retryKubectl(ctx, namespace, args, func(args []string) error { return Run(ctx, "kubectl", args...) })
}

// KubectlOut runs kubectl with the default context and the application namespace and returns its combined output.
func KubectlOut(ctx context.Context, namespace string, args ...string) (out string, err error) {
	err = retryKubectl(ctx, namespace, args, func(args []string) error {
		out, err = Output(ctx, "kubectl", args...)
		return err
	})
	return out, err
}

func retryKubectl(ctx context.Context, namespace string, args []string, f func([]string) error) error {
	getT(ctx).Helper()
	var ks []string
	if namespace != "" {
		// Add --namespace <namespace> before first flag argument
		ks = make([]string, 0, len(args)+2)
		pos := -1
		for i, arg := range args {
			if strings.HasPrefix(arg, "-") {
				pos = i
				break
			}
			ks = append(ks, arg)
		}
		ks = append(ks, "--namespace", namespace)
		if pos >= 0 {
			ks = append(ks, args[pos:]...)
		}
		args = ks
	}
	b := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(2*time.Second),
		backoff.WithMaxInterval(7*time.Second),
		backoff.WithMaxElapsedTime(30*time.Second),
	)
	return backoff.Retry(func() error {
		err := f(args)
		if err != nil && !strings.Contains(err.Error(), "(ServiceUnavailable)") {
			err = backoff.Permanent(err)
		}
		return err
	}, b)
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
		if t.Failed() {
			if out, err := KubectlOut(ctx, ns, "get", "events", "--field-selector", "type!=Normal"); err == nil {
				dlog.Debugf(ctx, "Events where type != Normal from namespace %s\n%s", ns, out)
			}
		}
		go func(ns string) {
			defer wg.Done()
			assert.NoError(t, Kubectl(ctx, "", "delete", "namespace", "--wait=false", ns))
		}(ns)
	}
	wg.Wait()
}

// StartLocalHttpEchoServerWithAddr is like StartLocalHttpEchoServer but binds to a specific host instead of localhost.
func StartLocalHttpEchoServerWithAddr(ctx context.Context, name, addr string) (int, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", addr)
	require.NoError(getT(ctx), err, "failed to listen on localhost")
	sc := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ioutil.Printf(w, "%s from intercept at %s", name, r.URL.Path)
		}),
	}
	go func() {
		_ = sc.Serve(l)
	}()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		err = sc.Shutdown(ctx)
		if err != nil {
			dlog.Errorf(ctx, "http server on %s exited with error: %v", addr, err)
		} else {
			dlog.Errorf(ctx, "http server on %s exited", addr)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, cancel
}

// StartLocalHttpEchoServer starts a local http server that echoes a line with the given name and
// the current URL path. The port is returned together with function that cancels the server.
func StartLocalHttpEchoServer(ctx context.Context, name string) (int, context.CancelFunc) {
	return StartLocalHttpEchoServerWithAddr(ctx, name, "localhost:0")
}

// PingInterceptedEchoServer assumes that a server has been created using StartLocalHttpEchoServer and
// that an intercept is active for the given svc and svcPort that will redirect to that local server.
func PingInterceptedEchoServer(ctx context.Context, svc, svcPort string, headers ...string) {
	wl := svc
	if slashIdx := strings.IndexByte(svc, '/'); slashIdx > 0 {
		wl = svc[slashIdx+1:]
		svc = svc[:slashIdx]
	}
	expectedOutput := fmt.Sprintf("%s from intercept at /", wl)
	dlog.Infof(ctx, "pinging %s, expecting output: %s", net.JoinHostPort(svc, svcPort), expectedOutput)

	ping := func() bool {
		// condition
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", svc)
		if err != nil {
			dlog.Info(ctx, err)
			return false
		}
		ips = iputil.UniqueSorted(ips)
		if len(ips) != 1 {
			dlog.Infof(ctx, "Lookup for %s returned %v", svc, ips)
			return false
		}

		hc := http.Client{Timeout: 2 * time.Second}
		rq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s", net.JoinHostPort(ips[0].String(), svcPort)), nil)
		if err != nil {
			dlog.Info(ctx, err)
			return false
		}
		for _, h := range headers {
			kv := strings.SplitN(h, "=", 2)
			rq.Header[kv[0]] = []string{kv[1]}
		}
		resp, err := hc.Do(rq)
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
	}
	if ping() {
		return
	}
	require.Eventually(getT(ctx), ping, time.Minute, 5*time.Second, `body of %q equals %q`, "http://"+svc, expectedOutput)
}

func WithConfig(c context.Context, modifierFunc func(config client.Config)) context.Context {
	// Quit a running daemon. We're changing the directory where its config resides.
	TelepresenceQuitOk(c)

	t := getT(c)
	cfgVal := reflect.ValueOf(client.GetConfig(c)).Elem()
	cfgCopyVal := reflect.New(cfgVal.Type())
	cfgCopyVal.Elem().Set(cfgVal) // By value copy
	configCopy := cfgCopyVal.Interface()
	modifierFunc(configCopy.(client.Config))
	configYaml, err := configCopy.(client.Config).MarshalYAML()
	require.NoError(t, err)
	configYamlStr := string(configYaml)
	configDir, err := os.MkdirTemp(t.TempDir(), "config")
	require.NoError(t, err)
	c, err = SetConfig(c, configDir, configYamlStr)
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

	em := cluster.Extensions
	if em == nil {
		em = map[string]k8sruntime.Object{}
	}
	raw, err := json.Marshal(extProducer(cluster))
	require.NoError(t, err, "unable to json.Marshal extension map")
	em["telepresence.io"] = &k8sruntime.Unknown{Raw: raw}
	cluster.Extensions = em

	kctx := *cc
	kctx.Cluster = "extra"
	cfg = &api.Config{
		Kind:           "Config",
		APIVersion:     "v1",
		Preferences:    api.Preferences{},
		Clusters:       map[string]*api.Cluster{"extra": cluster},
		Contexts:       map[string]*api.Context{"extra": &kctx},
		CurrentContext: "extra",
	}
	kubeconfigFileName := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*cfg, kubeconfigFileName), "unable to write modified kubeconfig")
	return WithEnv(ctx, map[string]string{"KUBECONFIG": strings.Join([]string{kc, kubeconfigFileName}, string([]byte{os.PathListSeparator}))})
}

func WithKubeConfig(ctx context.Context, cfg *api.Config) context.Context {
	t := getT(ctx)
	kubeconfigFileName := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, clientcmd.WriteToFile(*cfg, kubeconfigFileName), "unable to write modified kubeconfig")
	return WithEnv(ctx, map[string]string{"KUBECONFIG": kubeconfigFileName})
}

func RunningPods(ctx context.Context, svc, ns string) []core.Pod {
	out, err := KubectlOut(ctx, ns, "get", "pods", "-o", "json", "--field-selector", "status.phase==Running", "-l", "app="+svc)
	if err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	var pm core.PodList
	if err := json.UnmarshalRead(strings.NewReader(out), &pm); err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	return slices.DeleteFunc(pm.Items, func(pod core.Pod) bool {
		for _, cn := range pod.Status.ContainerStatuses {
			if r := cn.State.Running; r != nil && !r.StartedAt.IsZero() {
				// At least one container is still running.
				return false
			}
		}
		return true
	})
}

// RunningPodNames return the names of running pods with app=<service name>. Running here means
// that at least one container is still running. I.e. the pod might well be terminating
// but still considered running.
func RunningPodNames(ctx context.Context, svc, ns string) []string {
	pods := RunningPods(ctx, svc, ns)
	podNames := make([]string, len(pods))
	for i := range pods {
		podNames[i] = pods[i].Name
	}
	dlog.Infof(ctx, "Running pods %v", podNames)
	return podNames
}

// RunningPodsWithAgents returns the names of running pods with a matching appPrefix that
// has a running traffic-agent container.
func RunningPodsWithAgents(ctx context.Context, appPrefix, ns string) []string {
	out, err := KubectlOut(ctx, ns, "get", "pods", "-o", "json", "--field-selector", "status.phase==Running")
	if err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	var pm core.PodList
	if err := json.UnmarshalRead(strings.NewReader(out), &pm); err != nil {
		getT(ctx).Log(err.Error())
		return nil
	}
	pods := make([]string, 0, len(pm.Items))
nextPod:
	for _, pod := range pm.Items {
		if !strings.HasPrefix(pod.Labels["app"], appPrefix) {
			continue
		}
		for _, cn := range pod.Status.ContainerStatuses {
			if cn.Name == agentconfig.ContainerName && cn.Ready {
				if r := cn.State.Running; r != nil && !r.StartedAt.IsZero() {
					pods = append(pods, pod.Name)
					continue nextPod
				}
			}
		}
	}
	return pods
}
