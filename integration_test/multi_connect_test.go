package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type multiConnectSuite struct {
	itest.Suite
	itest.NamespacePair
	appSpace2  string
	mgrSpace2  string
	handlerTag string
}

func (s *multiConnectSuite) SuiteName() string {
	return "MultiConnect"
}

func init() {
	// This will give us one namespace pair with a traffic-manager installed.
	itest.AddTrafficManagerSuite("-1", func(h itest.NamespacePair) itest.TestingSuite {
		return &multiConnectSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *multiConnectSuite) SetupSuite() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}
	s.Suite.SetupSuite()
	// This will give us another namespace pair with a traffic-manager installed.
	ctx := s.Context()
	require := s.Require()
	suffix := itest.GetGlobalHarness(s.HarnessContext()).Suffix()
	s.appSpace2, s.mgrSpace2 = itest.AppAndMgrNSName(suffix + "-2")
	itest.CreateNamespaces(ctx, s.appSpace2, s.mgrSpace2)

	const svc = "echo"
	appData := itest.AppData{
		AppName: svc,
		Image:   "jmalloc/echo-server:0.1.0",
		Ports: []itest.AppPort{
			{
				ServicePortNumber: 80,
				TargetPortName:    "http",
				TargetPortNumber:  8080,
			},
		},
		Env: map[string]string{"PORT": "8080"},
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		itest.ApplyAppTemplate(ctx, s.AppNamespace(), &appData)
	}()
	go func() {
		defer wg.Done()
		itest.ApplyAppTemplate(ctx, s.appSpace2, &appData)
	}()

	ctx2 := itest.WithNamespaces(ctx, &itest.Namespaces{Namespace: s.mgrSpace2, ManagedNamespaces: []string{s.appSpace2}})
	err := itest.Kubectl(ctx2, s.mgrSpace2, "apply", "-f", filepath.Join(itest.GetOSSRoot(ctx2), "testdata", "k8s", "client_sa.yaml"))
	require.NoError(err, "failed to create connect ServiceAccount")

	ctx2 = itest.WithUser(ctx2, s.mgrSpace2+":"+itest.TestUser)
	require.NoError(s.TelepresenceHelmInstall(ctx2, false))
	itest.TelepresenceQuitOk(ctx2)

	s.handlerTag = "telepresence/echo-test"
	testDir := "testdata/echo-server"
	_, err = itest.Output(ctx, "docker", "build", "-t", s.handlerTag, testDir)
	require.NoError(err)
	wg.Wait()
	if s.T().Failed() {
		s.T().FailNow()
	}
}

func (s *multiConnectSuite) TearDownSuite() {
	ctx2 := itest.WithNamespaces(s.Context(), &itest.Namespaces{Namespace: s.mgrSpace2, ManagedNamespaces: []string{s.appSpace2}})
	s.UninstallTrafficManager(ctx2, s.mgrSpace2)
	itest.DeleteNamespaces(ctx2, s.appSpace2, s.mgrSpace2)
}

func (s *multiConnectSuite) Test_MultipleConnect() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "connect", "--docker", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	defer itest.TelepresenceDisconnectOk(ctx, "--use", s.AppNamespace())
	ctx2 := itest.WithUser(ctx, s.mgrSpace2+":"+itest.TestUser)
	itest.TelepresenceOk(ctx2, "connect", "--docker", "--namespace", s.appSpace2, "--manager-namespace", s.mgrSpace2)
	defer itest.TelepresenceDisconnectOk(ctx2, "--use", s.appSpace2)

	require := s.Require()
	kc := itest.KubeConfig(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	require.NoError(err)
	ctxName := ioutil.SafeName(cfg.CurrentContext)
	s.doubleConnectCheck(ctx, ctx2, ctxName+"-"+s.AppNamespace()+"-cn", ctxName+"-"+s.appSpace2+"-cn", s.AppNamespace(), s.appSpace2, "")
}

func (s *multiConnectSuite) Test_MultipleConnect_named() {
	ctx := s.Context()
	const n1 = "primary"
	const n2 = "secondary"
	itest.TelepresenceOk(ctx, "connect", "--docker", "--name", n1, "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	defer itest.TelepresenceDisconnectOk(ctx, "--use", n1)

	ctx2 := itest.WithUser(ctx, s.mgrSpace2+":"+itest.TestUser)
	itest.TelepresenceOk(ctx2, "connect", "--docker", "--name", n2, "--namespace", s.appSpace2, "--manager-namespace", s.mgrSpace2)
	defer itest.TelepresenceDisconnectOk(ctx2, "--use", n2)

	s.doubleConnectCheck(ctx, ctx2, n1, n2, s.AppNamespace(), s.appSpace2, "")
}

// Test_MultipleConnect_sameNamespace tests that we can have multiple connects to the same namespace when using
// named connections. This can be of interest when ports on localhost in different intercepted pods collide but can't
// be remapped because the intercept handler expects them to be specific numbers. Using multiple connections and
// docker containers means that each container have its own localhost.
func (s *multiConnectSuite) Test_MultipleConnect_sameNamespace() {
	ctx := s.Context()
	const n1 = "first"
	const n2 = "second"
	s.TelepresenceConnect(ctx, "--docker", "--name", n1)
	defer itest.TelepresenceDisconnectOk(ctx, "--use", n1)

	s.TelepresenceConnect(ctx, "--docker", "--name", n2)
	defer itest.TelepresenceDisconnectOk(ctx, "--use", n2)

	itest.ApplyEchoService(ctx, "hello", s.AppNamespace(), 80)

	s.doubleConnectCheck(ctx, ctx, n1, n2, s.AppNamespace(), s.AppNamespace(), "hello")
}

func (s *multiConnectSuite) doubleConnectCheck(ctx1, ctx2 context.Context, n1, n2, ns1, ns2, svc2 string) {
	require := s.Require()

	st := itest.TelepresenceStatusOk(ctx1, "--use", n1)
	require.Equal(st.UserDaemon.Namespace, ns1)
	name1 := st.UserDaemon.Name

	st = itest.TelepresenceStatusOk(ctx1, "--use", n2)
	require.Equal(st.UserDaemon.Namespace, ns2)
	name2 := st.UserDaemon.Name

	cacheDir := filelocation.AppUserCacheDir(ctx1)
	var di daemon.Info

	info, err := os.ReadFile(filepath.Join(cacheDir, "daemons", n1+".json"))
	require.NoError(err)
	require.NoError(json.Unmarshal(info, &di))
	require.Equal(ns1, di.Namespace)

	info, err = os.ReadFile(filepath.Join(cacheDir, "daemons", n2+".json"))
	require.NoError(err)
	require.NoError(json.Unmarshal(info, &di))
	require.Equal(ns2, di.Namespace)

	ctx1, cancel1 := context.WithCancel(ctx1)
	defer cancel1()

	ctx2, cancel2 := context.WithCancel(ctx2)
	defer cancel2()

	wg := sync.WaitGroup{}
	runDockerRun := func(ctx context.Context, use, svc string) {
		defer wg.Done()
		_, _, _ = itest.Telepresence(ctx, "intercept", "--use", use, "--mount", "false", svc, "--docker-run", "--port", "8080", "--", "--rm", "--name", use, s.handlerTag)
	}

	assertInterceptResponse := func(ctx context.Context, cn, svc string) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--use", cn, "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		}, 30*time.Second, 3*time.Second)

		// Response contains env variables TELEPRESENCE_CONTAINER and TELEPRESENCE_INTERCEPT_ID
		expectedOutput := regexp.MustCompile(`Intercept id [0-9a-f-]+:` + svc)
		s.Eventually(
			// condition
			func() bool {
				out, err := itest.Output(ctx,
					"docker", "run", "--network", "container:"+"tp-"+cn, "--rm", "curlimages/curl", "--silent", "--max-time", "1", svc)
				if err != nil {
					dlog.Errorf(ctx, "%s:%v", out, err)
					return false
				}
				dlog.Info(ctx, out)
				return expectedOutput.MatchString(out)
			},
			10*time.Second, // waitFor
			2*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
		)
	}

	assertNotIntercepted := func(ctx context.Context, use, svc string) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--use", use, "--intercepts")
			return err == nil && !strings.Contains(stdout, svc+": intercepted")
		}, 10*time.Second, 2*time.Second)
	}

	svc1 := "echo"
	if svc2 == "" {
		svc2 = svc1
	}
	wg.Add(2)
	go runDockerRun(ctx1, n1, svc1)
	go runDockerRun(ctx2, n2, svc2)

	assertInterceptResponse(ctx1, name1, svc1)
	assertInterceptResponse(ctx2, name2, svc2)

	itest.TelepresenceOk(ctx1, "leave", "--use", n1, svc1)
	assertNotIntercepted(ctx1, n1, svc1)

	// Other connection's intercept is still alive and kicking.
	assertInterceptResponse(ctx2, name2, svc2)

	itest.TelepresenceOk(ctx2, "leave", "--use", n2, svc2)
	assertNotIntercepted(ctx2, n2, svc2)

	cancel1()
	cancel2()
	wg.Wait()
}
