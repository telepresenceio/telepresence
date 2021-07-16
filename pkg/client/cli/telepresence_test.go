package cli_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	_ "github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// serviceCount is the number of interceptable services that gets installed
// in the cluster and later intercepted
const serviceCount = 3

func TestTelepresence(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	dtest.WithMachineLock(ctx, func(ctx context.Context) {
		suite.Run(t, new(telepresenceSuite))
	})
}

type telepresenceSuite struct {
	suite.Suite
	testVersion          string
	namespace            string
	managerTestNamespace string
}

func (ts *telepresenceSuite) SetupSuite() {
	// Check that the "ko" program exists, and adjust PATH as necessary.
	if info, err := os.Stat("../../../tools/bin/ko"); err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0100) == 0 {
		ts.FailNow("it looks like the ./tools/bin/ko executable wasn't built; be sure to build it with `make` before running `go test`!")
	}
	require := ts.Require()
	toolbindir, err := filepath.Abs("../../../tools/bin")
	require.NoError(err)
	_ = os.Chdir("../../..")

	os.Setenv("PATH", toolbindir+":"+os.Getenv("PATH"))

	// Remove very verbose output from DTEST initialization
	log.SetOutput(ioutil.Discard)

	ts.testVersion = fmt.Sprintf("v2.0.0-gotest.%d", os.Getpid())
	ts.namespace = fmt.Sprintf("telepresence-%d", os.Getpid())
	ts.managerTestNamespace = fmt.Sprintf("ambassador-%d", os.Getpid())

	version.Version = ts.testVersion

	ctx := dlog.NewTestContext(ts.T(), false)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		executable, err := ts.buildExecutable(ctx)
		ts.NoError(err)
		client.SetExe(executable)
	}()

	_ = os.Remove(client.ConnectorSocketName)
	err = run(ctx, "sudo", "true")
	require.NoError(err, "acquire privileges")

	registry := dtest.DockerRegistry(ctx)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)
	os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", ts.managerTestNamespace)
	os.Setenv("DTEST_REGISTRY", registry) // Prevent calls to dtest.RegistryUp() which may panic

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := ts.publishManager()
		ts.NoError(err)
	}()

	wg.Wait()

	// We do this after the above goroutines are finished, instead of in parallel with them,
	// because there seems to be a bug where buildExecutable sometimes modifies the kubeconfig and
	// removes the telepresence-test-user that is created in this function.
	ts.setupKubeConfig(ctx)

	wg.Add(serviceCount)
	for i := 0; i < serviceCount; i++ {
		i := i
		go func() {
			defer wg.Done()
			err = ts.applyEchoService(ctx, fmt.Sprintf("hello-%d", i))
			ts.NoError(err)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = ts.applyApp(ctx, "with-probes", "with-probes", 80)
		ts.NoError(err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = ts.applyApp(ctx, "rs-echo", "rs-echo", 80)
		ts.NoError(err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = ts.applyApp(ctx, "ss-echo", "ss-echo", 80)
		ts.NoError(err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = ts.applyApp(ctx, "echo-w-sidecars", "echo-w-sidecars", 80)
		ts.NoError(err)
	}()

	wg.Wait()

	// Ensure that telepresence is not logged in
	_, _ = telepresence(ts.T(), "logout")

	// Ensure that no telepresence is running when the tests start
	_, _ = telepresence(ts.T(), "quit")
}

func (ts *telepresenceSuite) TearDownSuite() {
	ctx := dlog.NewTestContext(ts.T(), false)
	_ = run(ctx, "kubectl", "config", "use-context", "default")
	_ = run(ctx, "kubectl", "delete", "namespace", ts.namespace)
	_ = run(ctx, "kubectl", "delete", "mutatingwebhookconfiguration", "agent-injector-webhook-"+ts.managerTestNamespace)
	_ = run(ctx, "kubectl", "delete", "namespace", ts.managerTestNamespace)
	// Undo RBAC things
	_ = run(ctx, "kubectl", "delete", "-f", "k8s/client_rbac.yaml")
	_ = run(ctx, "kubectl", "config", "delete-context", "telepresence-test-developer")
	_ = run(ctx, "kubectl", "config", "delete-user", "telepresence-test-developer")
}

func (ts *telepresenceSuite) TestA_WithNoDaemonRunning() {
	ts.Run("Version", func() {
		stdout, stderr := telepresence(ts.T(), "version")
		ts.Empty(stderr)
		ts.Contains(stdout, fmt.Sprintf("Client: %s", client.DisplayVersion()))
	})
	ts.Run("Status", func() {
		out, _ := telepresence(ts.T(), "status")
		ts.Contains(out, "Root Daemon: Not running")
		ts.Contains(out, "User Daemon: Not running")
	})

	ts.Run("Connect using invalid KUBECONFIG", func() {
		ts.Run("Reports config error and exits", func() {
			kubeConfig := os.Getenv("KUBECONFIG")
			defer os.Setenv("KUBECONFIG", kubeConfig)
			os.Setenv("KUBECONFIG", "/dev/null")
			stdout, stderr := telepresence(ts.T(), "connect")
			ts.Contains(stderr, "kubeconfig has no context definition")
			ts.Contains(stdout, "Launching Telepresence Daemon")
			ts.Contains(stdout, "Daemon quitting")
		})
	})

	ts.Run("Connect with non existing context", func() {
		ts.Run("Reports connect error and exits", func() {
			stdout, stderr := telepresence(ts.T(), "connect", "--context", "not-likely-to-exist")
			ts.Contains(stderr, `"not-likely-to-exist" does not exist`)
			ts.Contains(stdout, "Launching Telepresence Daemon")
			ts.Contains(stdout, "Daemon quitting")
		})
	})

	ts.Run("Connect with a command", func() {
		ts.Run("Connects, executes the command, and then exits", func() {
			stdout, stderr := telepresence(ts.T(), "connect", "--", client.GetExe(), "status")
			require := ts.Require()
			require.Empty(stderr)
			require.Contains(stdout, "Launching Telepresence Daemon")
			require.Contains(stdout, "Connected to context")
			require.Contains(stdout, "Kubernetes context:")
			require.Regexp(`Telepresence proxy:\s+ON`, stdout)
			require.Contains(stdout, "Daemon quitting")
		})
	})

	ts.Run("Root Daemon Log Level", func() {
		t := ts.T()
		require := ts.Require()

		configDir := t.TempDir()

		ctx := dlog.NewTestContext(t, false)
		registry := dtest.DockerRegistry(ctx)
		configYml := fmt.Sprintf("logLevels:\n  rootDaemon: debug\nimages:\n  registry: %s\n", registry)
		ctx, err := setConfig(ctx, configDir, configYml)
		require.NoError(err)

		logDir := t.TempDir()
		ctx = filelocation.WithAppUserLogDir(ctx, logDir)
		_, stderr := telepresenceContext(ctx, "connect")
		require.Empty(stderr)
		_, stderr = telepresenceContext(ctx, "quit")
		require.Empty(stderr)
		rootLog, err := os.Open(filepath.Join(logDir, "daemon.log"))
		require.NoError(err)
		defer rootLog.Close()

		hasDebug := false
		scn := bufio.NewScanner(rootLog)
		match := regexp.MustCompile(` debug +daemon/server`)
		for scn.Scan() && !hasDebug {
			hasDebug = match.MatchString(scn.Text())
		}
		ts.True(hasDebug, "daemon.log does not contain expected debug statements")
	})

	ts.Run("DNS includes", func() {
		t := ts.T()
		require := ts.Require()

		tmpDir := t.TempDir()
		origKubeconfigFileName := os.Getenv("DTEST_KUBECONFIG")
		kubeconfigFileName := filepath.Join(tmpDir, "kubeconfig")

		var cfg *api.Config
		cfg, err := clientcmd.LoadFromFile(origKubeconfigFileName)
		require.NoError(err, "Unable to read DTEST_KUBECONFIG")
		require.NoError(err, api.MinifyConfig(cfg), "unable to minify config")
		var cluster *api.Cluster
		for _, c := range cfg.Clusters {
			cluster = c
			break
		}
		require.NotNilf(cluster, "unable to get cluster from config")
		cluster.Extensions = map[string]k8sruntime.Object{"telepresence.io": &k8sruntime.Unknown{
			Raw: []byte(`{"dns":{"include-suffixes": [".org"]}}`),
		}}

		require.NoError(clientcmd.WriteToFile(*cfg, kubeconfigFileName), "unable to write modified kubeconfig")

		ctx := dlog.NewTestContext(t, false)
		registry := dtest.DockerRegistry(ctx)
		configYml := fmt.Sprintf("logLevels:\n  rootDaemon: debug\nimages:\n  registry: %s\n", registry)
		ctx, err = setConfig(ctx, tmpDir, configYml)
		require.NoError(err)

		defer os.Setenv("KUBECONFIG", origKubeconfigFileName)
		os.Setenv("KUBECONFIG", kubeconfigFileName)
		ctx = filelocation.WithAppUserLogDir(ctx, tmpDir)
		_, stderr := telepresenceContext(ctx, "connect")
		require.Empty(stderr)
		_ = run(ctx, "curl", "--silent", "example.org")

		_, stderr = telepresenceContext(ctx, "quit")
		require.Empty(stderr)
		rootLog, err := os.Open(filepath.Join(tmpDir, "daemon.log"))
		require.NoError(err)
		defer rootLog.Close()

		hasLookup := false
		scn := bufio.NewScanner(rootLog)
		for scn.Scan() && !hasLookup {
			hasLookup = strings.Contains(scn.Text(), `LookupHost "example.org"`)
		}
		ts.True(hasLookup, "daemon.log does not contain expected LookupHost statement")
	})

	ts.Run("Webhook Agent Image From Config", func() {
		t := ts.T()
		require := ts.Require()
		uninstallTrafficManager := func() {
			stdout, stderr := telepresence(t, "uninstall", "--everything")
			require.Empty(stderr)
			require.Contains(stdout, "Daemon quitting")
		}
		// Remove the traffic-manager since we are altering config that applies to
		// creating the traffic-manager
		uninstallTrafficManager()
		stdout, stderr := telepresence(t, "uninstall", "--everything")
		require.Empty(stderr)
		require.Contains(stdout, "Daemon quitting")

		configDir := t.TempDir()

		// Use a config with agentImage and webhookAgentImage to validate that its the
		// latter that is used in the traffic-manager
		ctx := dlog.NewTestContext(t, false)
		registry := dtest.DockerRegistry(ctx)
		configYml := fmt.Sprintf("images:\n  registry: %s\n  agentImage: notUsed:0.0.1\n  webhookAgentImage: imageFromConfig:0.0.1\n  webhookRegistry: %s", registry, registry)
		ctx, err := setConfig(ctx, configDir, configYml)
		require.NoError(err)

		_, stderr = telepresenceContext(ctx, "connect")
		require.Empty(stderr)

		// When this function ends we uninstall the manager,
		// connect again to establish a traffic-manager without
		// our edited config, and then quit our connection to the manager.
		defer func() {
			uninstallTrafficManager()
			_, stderr = telepresence(t, "connect")
			require.Empty(stderr)
			_, stderr = telepresence(t, "quit")
			require.Empty(stderr)
		}()

		image, err := ts.kubectlOut(ctx, "get",
			"--namespace", ts.managerTestNamespace,
			"deploy", "traffic-manager",
			"--ignore-not-found",
			"-o",
			"jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TELEPRESENCE_AGENT_IMAGE')].value}")
		require.NoError(err)
		desiredImage := fmt.Sprintf("%s/imageFromConfig:0.0.1", registry)
		ts.Equal(desiredImage, image)
	})
}

func (ts *telepresenceSuite) TestB_Connected() {
	suite.Run(ts.T(), &connectedSuite{tpSuite: ts})
}

func (ts *telepresenceSuite) TestC_Uninstall() {
	ts.Run("Uninstalls the traffic manager and quits", func() {
		require := ts.Require()
		ctx := dlog.NewTestContext(ts.T(), false)
		names := func() (string, error) {
			return ts.kubectlOut(ctx, "get",
				"--namespace", ts.managerTestNamespace,
				"svc,deploy", "traffic-manager",
				"--ignore-not-found",
				"-o", "jsonpath={.items[*].metadata.name}")
		}
		stdout, err := names()
		require.NoError(err)
		require.Equal(2, len(strings.Split(stdout, " "))) // The service and the deployment

		// The telepresence-test-developer will not be able to uninstall everything
		require.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
		stdout, stderr := telepresence(ts.T(), "uninstall", "--everything")
		require.Empty(stderr)
		require.Contains(stdout, "Daemon quitting")
		require.Eventually(
			func() bool {
				stdout, _ := names()
				return stdout == ""
			},
			5*time.Second,        // waitFor
			500*time.Millisecond, // polling interval
		)
	})
}

func (ts *telepresenceSuite) TestD_HelmChart() {
	suite.Run(ts.T(), &helmSuite{tpSuite: ts})
}

type connectedSuite struct {
	suite.Suite
	tpSuite *telepresenceSuite
}

func (cs *connectedSuite) ns() string {
	return cs.tpSuite.namespace
}

func (cs *connectedSuite) SetupSuite() {
	require := cs.Require()
	c := dlog.NewTestContext(cs.T(), false)

	cs.Eventually(func() bool {
		return run(c, "kubectl", "config", "use-context", "telepresence-test-developer") == nil
	}, 10*time.Second, time.Second)

	stdout, stderr := telepresence(cs.T(), "connect")
	require.Empty(stderr)
	require.Contains(stdout, "Connected to context")

	// Give outbound interceptor 15 seconds to kick in.
	require.Eventually(
		// condition
		func() bool {
			stdout, _ := telepresence(cs.T(), "status")
			return regexp.MustCompile(`Telepresence proxy:\s+ON`).FindString(stdout) != ""
		},
		15*time.Second, // waitFor
		time.Second,    // polling interval
		"Timeout waiting for network overrides to establish", // msg
	)
}

func (cs *connectedSuite) TearDownSuite() {
	stdout, stderr := telepresence(cs.T(), "quit")
	cs.Empty(stderr)
	cs.Contains(stdout, "quitting")
	c := dlog.NewTestContext(cs.T(), false)
	cs.NoError(cs.tpSuite.kubectl(c, "config", "use-context", "default"))
	time.Sleep(time.Second) // Allow some time for processes to die and sockets to vanish
}

func (cs *connectedSuite) TestA_ReportsVersionFromDaemon() {
	stdout, stderr := telepresence(cs.T(), "version")
	cs.Empty(stderr)
	vs := client.DisplayVersion()
	cs.Contains(stdout, fmt.Sprintf("Client: %s", vs))
	cs.Contains(stdout, fmt.Sprintf("Root Daemon: %s", vs))
	cs.Contains(stdout, fmt.Sprintf("User Daemon: %s", vs))
}

func (cs *connectedSuite) TestB_ReportsStatusAsConnected() {
	stdout, stderr := telepresence(cs.T(), "status")
	cs.Empty(stderr)
	cs.Contains(stdout, "Kubernetes context:")
}

func (cs *connectedSuite) TestC_ProxiesOutboundTraffic() {
	ctx := dlog.NewTestContext(cs.T(), false)
	for i := 0; i < serviceCount; i++ {
		svc := fmt.Sprintf("hello-%d.%s", i, cs.ns())
		expectedOutput := fmt.Sprintf("Request served by hello-%d", i)
		cs.Require().Eventually(
			// condition
			func() bool {
				dlog.Infof(ctx, "trying %q...", "http://"+svc)
				hc := http.Client{Timeout: time.Second}
				resp, err := hc.Get("http://" + svc)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				defer resp.Body.Close()
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				dlog.Infof(ctx, "body: %q", body)
				return strings.Contains(string(body), expectedOutput)
			},
			15*time.Second, // waitfor
			3*time.Second,  // polling interval
			`body of %q contains %q`, "http://"+svc, expectedOutput,
		)
	}
}

func (cs *connectedSuite) TestD_Intercepted() {
	suite.Run(cs.T(), &interceptedSuite{tpSuite: cs.tpSuite})
}

func (cs *connectedSuite) TestE_PodWithSubdomain() {
	require := cs.Require()
	c := dlog.NewTestContext(cs.T(), false)
	require.NoError(cs.tpSuite.applyApp(c, "echo-w-subdomain", "echo.subsonic", 8080))
	defer func() {
		cs.NoError(cs.tpSuite.kubectl(c, "delete", "svc", "subsonic", "--context", "default"))
		cs.NoError(cs.tpSuite.kubectl(c, "delete", "deploy", "echo-subsonic", "--context", "default"))
	}()

	cc, cancel := context.WithTimeout(c, 3*time.Second)
	defer cancel()
	ip, err := net.DefaultResolver.LookupHost(cc, "echo.subsonic."+cs.ns())
	cs.NoError(err)
	cs.Equal(1, len(ip))
	ip, err = net.DefaultResolver.LookupHost(cc, "echo.subsonic."+cs.ns()+".svc.cluster.local")
	cs.NoError(err)
	cs.Equal(1, len(ip))
}

func (cs *connectedSuite) TestF_SuccessfullyInterceptsDeploymentWithProbes() {
	defer telepresence(cs.T(), "leave", "with-probes-"+cs.ns())

	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "with-probes", "--port", "9090")
	require.Empty(stderr)
	require.Contains(stdout, "Using Deployment with-probes")
	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "with-probes: intercepted")
}

func (cs *connectedSuite) TestG_SuccessfullyInterceptsReplicaSet() {
	defer telepresence(cs.T(), "leave", "rs-echo-"+cs.ns())

	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "rs-echo", "--port", "9091")
	require.Empty(stderr)
	require.Contains(stdout, "Using ReplicaSet rs-echo")
	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "rs-echo: intercepted")
}

func (cs *connectedSuite) TestH_SuccessfullyInterceptsStatefulSet() {
	defer telepresence(cs.T(), "leave", "ss-echo-"+cs.ns())

	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "ss-echo", "--port", "9091")
	require.Empty(stderr)
	require.Contains(stdout, "Using StatefulSet ss-echo")
	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "ss-echo: intercepted")
}

func (cs *connectedSuite) TestI_LocalOnlyIntercept() {
	cs.Run("intercept can be established", func() {
		stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--local-only", "mylocal")
		cs.Empty(stdout)
		cs.Empty(stderr)
	})

	cs.Run("is included in list output", func() {
		// list includes local intercept
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
		cs.Empty(stderr)
		cs.Contains(stdout, "mylocal: local-only intercept")
	})

	cs.Run("makes services reachable using unqualified name", func() {
		ctx := dlog.NewTestContext(cs.T(), false)

		// service can be resolve with unqualified name
		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "ss-echo") == nil
		}, 3*time.Second, 1*time.Second)
	})

	cs.Run("leaving renders services unavailable using unqualified name", func() {
		stdout, stderr := telepresence(cs.T(), "leave", "mylocal")
		cs.Empty(stdout)
		cs.Empty(stderr)
		ctx := dlog.NewTestContext(cs.T(), false)
		cs.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			return run(ctx, "curl", "--silent", "ss-echo") != nil
		}, 3*time.Second, time.Second)
	})
}

func (cs *connectedSuite) TestJ_ListOnlyMapped() {
	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "connect", "--mapped-namespaces", "default")
	require.Empty(stderr)
	require.Empty(stdout)

	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns())
	require.Empty(stderr)
	require.Contains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")

	stdout, stderr = telepresence(cs.T(), "connect", "--mapped-namespaces", "all")
	require.Empty(stderr)
	require.Empty(stdout)

	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns())
	require.Empty(stderr)
	require.NotContains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
}

func (cs *connectedSuite) TestK_DockerRun() {
	require := cs.Require()
	ctx := dlog.NewTestContext(cs.T(), false)

	svc := "hello-0"
	tag := "telepresence/hello-test"
	testDir := "pkg/client/cli/testdata/hello"
	_, err := output(ctx, "docker", "build", "-t", tag, testDir)
	require.NoError(err)
	abs, err := filepath.Abs(testDir)
	require.NoError(err)

	// We need to explicitly set the default config since telepresenceContext
	// is being used below
	configDir := cs.T().TempDir()
	ctx, err = setDefaultConfig(ctx, configDir)
	require.NoError(err)

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableWithSoftness: true,
		ShutdownOnNonError: true,
	})

	grp.Go("server", func(ctx context.Context) error {
		stdout, _ := telepresenceContext(ctx, "intercept", "--namespace", cs.ns(), svc,
			"--docker-run", "--port", "8000", "--", "--rm", "-v", abs+":/usr/src/app", tag)
		cs.Contains(stdout, "Using Deployment "+svc)
		return nil
	})

	grp.Go("client", func(ctx context.Context) error {
		expectedOutput := "Hello from intercepted echo-server!"
		cs.Eventually(
			// condition
			func() bool {
				ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
				out, err := output(ctx, "curl", "--silent", svc)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				dlog.Info(ctx, out)
				return strings.Contains(out, expectedOutput)
			},
			30*time.Second, // waitFor
			1*time.Second,  // polling interval
			`body of %q equals %q`, "http://"+svc, expectedOutput,
		)
		return nil
	})

	cs.NoError(grp.Wait())
}

func (cs *connectedSuite) TestL_LegacySwapDeploymentDoesIntercept() {
	require := cs.Require()

	// We don't need to defer leaving the intercept because the
	// intercept is automatically left once the command is finished
	_, stderr := telepresence(cs.T(), "--swap-deployment", "with-probes", "--expose", "9090", "--namespace", cs.ns(), "--mount", "false", "--run", "sleep", "1")
	require.Contains(stderr, "Legacy Telepresence command used")
	require.Contains(stderr, "Using Deployment with-probes")

	// Since legacy Telepresence commands are detected and translated in the
	// RunSubcommands function, so we ensure that the help text is *not* being
	// printed out in this case.
	require.NotContains(stderr, "Telepresence can connect to a cluster and route all outbound traffic")

	// Verify that the intercept no longer exists
	stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
}

func (cs *connectedSuite) TestM_AutoInjectedAgent() {
	ctx := dlog.NewTestContext(cs.T(), false)
	cs.NoError(cs.tpSuite.applyApp(ctx, "echo-auto-inject", "echo-auto-inject", 80))
	defer func() {
		cs.NoError(cs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "echo-auto-inject", "--context", "default"))
	}()

	cs.Run("shows up with agent installed in list output", func() {
		cs.Eventually(func() bool {
			stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
			cs.Empty(stderr)
			return strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
		},
			10*time.Second, // waitFor
			2*time.Second,  // polling interval
		)
	})

	cs.Run("can be intercepted", func() {
		defer telepresence(cs.T(), "leave", "echo-auto-inject-"+cs.ns())

		require := cs.Require()
		stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "echo-auto-inject", "--port", "9091")
		require.Empty(stderr)
		require.Contains(stdout, "Using Deployment echo-auto-inject")
		stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
		require.Empty(stderr)
		require.Contains(stdout, "echo-auto-inject: intercepted")
	})
}

func (cs *connectedSuite) TestN_ToPodPortForwarding() {
	defer telepresence(cs.T(), "leave", "echo-w-sidecars-"+cs.ns())

	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "echo-w-sidecars", "--port", "8080", "--to-pod", "8081", "--to-pod", "8082")
	require.Empty(stderr)
	require.Contains(stdout, "Using Deployment echo-w-sidecars")
	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "echo-w-sidecars: intercepted")

	cs.Run("Forwarded port is reachable as localhost:PORT", func() {
		ctx := dlog.NewTestContext(cs.T(), false)

		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "localhost:8081") == nil
		}, 3*time.Second, 1*time.Second)

		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "localhost:8082") == nil
		}, 3*time.Second, 1*time.Second)
	})

	cs.Run("Non-forwarded port is not reachable", func() {
		ctx := dlog.NewTestContext(cs.T(), false)

		cs.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			return run(ctx, "curl", "--silent", "localhost:8083") != nil
		}, 3*time.Second, 1*time.Second)
	})
}

func (cs *connectedSuite) TestZ_Uninstall() {
	cs.Run("Uninstalls agent on given deployment", func() {
		require := cs.Require()
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
		require.Empty(stderr)
		require.Contains(stdout, "with-probes")
		_, stderr = telepresence(cs.T(), "uninstall", "--namespace", cs.ns(), "--agent", "with-probes")
		require.Empty(stderr)
		require.Eventually(
			// condition
			func() bool {
				stdout, _ := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
				return !strings.Contains(stdout, "with-probes")
			},
			30*time.Second, // waitFor
			2*time.Second,  // polling interval
		)
	})

	cs.Run("Uninstalls agent on given replicaset", func() {
		require := cs.Require()
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
		require.Empty(stderr)
		require.Contains(stdout, "rs-echo")
		_, stderr = telepresence(cs.T(), "uninstall", "--namespace", cs.ns(), "--agent", "rs-echo")
		require.Empty(stderr)
		require.Eventually(
			// condition
			func() bool {
				stdout, _ := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
				return !strings.Contains(stdout, "rs-echo")
			},
			30*time.Second, // waitFor
			2*time.Second,  // polling interval
		)
	})

	cs.Run("Uninstalls agent on given statefulset", func() {
		require := cs.Require()
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
		require.Empty(stderr)
		require.Contains(stdout, "ss-echo")
		_, stderr = telepresence(cs.T(), "uninstall", "--namespace", cs.ns(), "--agent", "ss-echo")
		require.Empty(stderr)
		require.Eventually(
			// condition
			func() bool {
				stdout, _ := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
				return !strings.Contains(stdout, "ss-echo")
			},
			30*time.Second, // waitFor
			2*time.Second,  // polling interval
		)
	})

	cs.Run("Uninstalls all agents", func() {
		require := cs.Require()
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
		require.Empty(stderr)
		require.GreaterOrEqual(len(strings.Split(stdout, "\n")), serviceCount)
		_, stderr = telepresence(cs.T(), "uninstall", "--namespace", cs.ns(), "--all-agents")
		require.Empty(stderr)
		require.Eventually(
			func() bool {
				stdout, _ := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--agents")
				return stdout == "No Workloads (Deployments, StatefulSets, or ReplicaSets)"
			},
			30*time.Second,     // waitFor
			2*time.Millisecond, // polling interval
		)
	})
}

type interceptedSuite struct {
	suite.Suite
	tpSuite        *telepresenceSuite
	intercepts     []string
	mountPoint     string // mount point for service 0.
	services       *dgroup.Group
	cancelServices context.CancelFunc
}

func (is *interceptedSuite) ns() string {
	return is.tpSuite.namespace
}

func (is *interceptedSuite) SetupSuite() {
	is.intercepts = make([]string, 0, serviceCount)
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(dlog.NewTestContext(is.T(), true)))
	is.services = dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	is.cancelServices = cancel

	is.Run("all intercepts ready", func() {
		rxs := make([]*regexp.Regexp, serviceCount)
		for i := 0; i < serviceCount; i++ {
			rxs[i] = regexp.MustCompile(fmt.Sprintf("hello-%d\\s*:\\s+ready to intercept", i))
		}
		is.Require().Eventually(
			// condition
			func() bool {
				stdout, _ := telepresence(is.T(), "list", "--namespace", is.ns())
				is.T().Log(stdout)
				for i := 0; i < serviceCount; i++ {
					if !rxs[i].MatchString(stdout) {
						return false
					}
				}
				return true
			},
			15*time.Second, // waitFor
			3*time.Second,  // polling interval
			`telepresence list reports all agents`,
		)
	})

	is.mountPoint = is.T().TempDir()
	is.Run("adding intercepts", func() {
		// Add all `hello-N` intercepts. Let `hello-0` have a mounted volume.
		addIntercept := func(i int, extraArgs ...string) {
			svc := fmt.Sprintf("hello-%d", i)
			port := strconv.Itoa(9000 + i)
			args := []string{"intercept", "--namespace", is.ns(), svc, "--port", port}
			stdout, stderr := telepresence(is.T(), append(args, extraArgs...)...)
			is.Require().Empty(stderr)
			is.intercepts = append(is.intercepts, svc)
			is.Contains(stdout, "Using Deployment "+svc)
		}
		addIntercept(0, "--mount", is.mountPoint)
		for i := 1; i < serviceCount; i++ {
			addIntercept(i, "--mount", "false")
		}
	})

	is.Run("starting http servers", func() {
		for i := 0; i < serviceCount; i++ {
			svc := fmt.Sprintf("hello-%d", i)
			port := strconv.Itoa(9000 + i)

			is.services.Go(svc, func(ctx context.Context) error {
				sc := &dhttp.ServerConfig{
					Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						fmt.Fprintf(w, "%s from intercept at %s", svc, r.URL.Path)
					}),
				}
				return sc.ListenAndServe(ctx, ":"+port)
			})
		}
	})
}

func (is *interceptedSuite) TearDownSuite() {
	for _, svc := range is.intercepts {
		stdout, stderr := telepresence(is.T(), "leave", svc+"-"+is.ns())
		is.Empty(stderr)
		is.Empty(stdout)
	}
	is.cancelServices()
	is.NoError(is.services.Wait())
	time.Sleep(time.Second) // Allow some time for processes to die and intercepts to vanish
}

func (is *interceptedSuite) TestA_VerifyingResponsesFromInterceptor() {
	for i := 0; i < serviceCount; i++ {
		svc := fmt.Sprintf("hello-%d", i)
		expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
		is.Require().Eventually(
			// condition
			func() bool {
				is.T().Logf("trying %q...", "http://"+svc)
				hc := http.Client{Timeout: time.Second}
				resp, err := hc.Get("http://" + svc)
				if err != nil {
					is.T().Log(err)
					return false
				}
				defer resp.Body.Close()
				is.T().Logf("status code: %v", resp.StatusCode)
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					is.T().Log(err)
					return false
				}
				is.T().Logf("body: %q", body)
				return string(body) == expectedOutput
			},
			15*time.Second, // waitFor
			3*time.Second,  // polling interval
			`body of %q equals %q`, "http://"+svc, expectedOutput,
		)
	}
}

func (is *interceptedSuite) TestB_ListingActiveIntercepts() {
	require := is.Require()
	stdout, stderr := telepresence(is.T(), "--namespace", is.ns(), "list", "--intercepts")
	require.Empty(stderr)
	for i := 0; i < serviceCount; i++ {
		require.Contains(stdout, fmt.Sprintf("hello-%d: intercepted", i))
	}
}

func (is *interceptedSuite) TestC_MountedFilesystem() {
	require := is.Require()
	st, err := os.Stat(is.mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(is.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

func (is *interceptedSuite) TestD_RestartInterceptedPod() {
	ts := is.tpSuite
	assert := is.Assert()
	require := is.Require()
	c := dlog.NewTestContext(is.T(), false)
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: hello-0-` + is.ns() + `\s+State\s*: ([^\n]+)\n`))

	// Scale down to zero pods
	require.NoError(ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "0"))

	// Verify that intercept remains but that no agent is found
	assert.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			dlog.Infof(c, "Got match '%s'", match[1])
			return match[1] == "WAITING" || strings.Contains(match[1], `No agent found for "hello-0"`)
		}
		return false
	}, 5*time.Second, time.Second)

	// Verify that volume mount is broken
	_, err := os.Stat(filepath.Join(is.mountPoint, "var"))
	assert.Error(err, "Stat on <mount point>/var succeeded although no agent was found")

	// Scale up again (start intercepted pod)
	assert.NoError(ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "1"))

	// Verify that intercept becomes active
	assert.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 10*time.Second, time.Second)

	// Verify that volume mount is restored
	assert.Eventually(func() bool {
		st, err := os.Stat(filepath.Join(is.mountPoint, "var"))
		return err == nil && st.IsDir()
	}, 5*time.Second, time.Second)
}

func (is *interceptedSuite) TestE_StopInterceptedPodOfMany() {
	ts := is.tpSuite
	assert := is.Assert()
	require := is.Require()
	c := dlog.NewTestContext(is.T(), false)
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: hello-0-` + is.ns() + `\s+State\s*: ([^\n]+)\n`))

	// Terminating is not a state, so you may want to wrap calls to this function in an eventually
	// to give any pods that are terminating the chance to complete.
	// https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/
	helloZeroPods := func() []string {
		pods, err := ts.kubectlOut(c, "get", "pods", "--field-selector", "status.phase==Running", "-l", "app=hello-0", "-o", "jsonpath={.items[*].metadata.name}")
		assert.NoError(err)
		pods = strings.TrimSpace(pods)
		dlog.Infof(c, "Pods = '%s'", pods)
		return strings.Split(pods, " ")
	}

	// Wait for exactly one active pod
	var currentPod string
	require.Eventually(func() bool {
		currentPods := helloZeroPods()
		if len(currentPods) == 1 {
			currentPod = currentPods[0]
			return true
		}
		return false
	}, 20*time.Second, 2*time.Second)

	// Scale up to two pods
	require.NoError(ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "2"))
	defer func() {
		_ = ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "1")
	}()

	// Wait for second pod to arrive
	assert.Eventually(func() bool { return len(helloZeroPods()) == 2 }, 5*time.Second, time.Second)

	// Delete the currently intercepted pod
	require.NoError(ts.kubectl(c, "--context", "default", "delete", "pod", currentPod))

	// Wait for that pod to disappear
	assert.Eventually(
		func() bool {
			for _, zp := range helloZeroPods() {
				if zp == currentPod {
					return false
				}
			}
			return true
		}, 5*time.Second, time.Second)

	// Verify that intercept is still active
	assert.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list", "--intercepts")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 5*time.Second, time.Second)

	// Verify response from intercepting client
	assert.Eventually(func() bool {
		hc := http.Client{Timeout: time.Second}
		resp, err := hc.Get("http://hello-0")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return "hello-0 from intercept at /" == string(body)
	}, 5*time.Second, time.Second)

	// Verify that volume mount is restored
	st, err := os.Stat(filepath.Join(is.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

type helmSuite struct {
	suite.Suite
	tpSuite           *telepresenceSuite
	managerNamespace1 string
	appNamespace1     string
	managerNamespace2 string
	appNamespace2     string
}

func (hs *helmSuite) SetupSuite() {
	ctx := dlog.NewTestContext(hs.T(), false)

	hs.appNamespace1 = hs.tpSuite.namespace
	hs.managerNamespace1 = hs.tpSuite.managerTestNamespace
	hs.appNamespace2 = fmt.Sprintf("telepresence-2-%d", os.Getpid())
	hs.managerNamespace2 = fmt.Sprintf("ambassador-2-%d", os.Getpid())

	hs.NoError(run(ctx, "kubectl", "create", "namespace", hs.appNamespace2))
	hs.NoError(run(ctx, "kubectl", "create", "namespace", hs.managerNamespace2))

	// These namespaces need to be labelled so that the webhook selector can find the namespaces
	hs.Eventually(func() bool {
		return run(ctx, "kubectl", "label", "--overwrite", "namespace", hs.appNamespace1, fmt.Sprintf("app.kubernetes.io/name=%s", hs.appNamespace1)) == nil
	}, 5*time.Second, time.Second)
	hs.Eventually(func() bool {
		return run(ctx, "kubectl", "label", "--overwrite", "namespace", hs.managerNamespace1, fmt.Sprintf("app.kubernetes.io/name=%s", hs.managerNamespace1)) == nil
	}, 5*time.Second, time.Second)
	hs.Eventually(func() bool {
		return run(ctx, "kubectl", "label", "--overwrite", "namespace", hs.appNamespace2, fmt.Sprintf("app.kubernetes.io/name=%s", hs.appNamespace2)) == nil
	}, 5*time.Second, time.Second)
	hs.Eventually(func() bool {
		return run(ctx, "kubectl", "label", "--overwrite", "namespace", hs.managerNamespace2, fmt.Sprintf("app.kubernetes.io/name=%s", hs.managerNamespace2)) == nil
	}, 5*time.Second, time.Second)

	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	// Uninstall to make sure we're starting fresh
	_, stderr := telepresence(hs.T(), "uninstall", "--everything")
	hs.Empty(stderr)

	// Destroy the telepresence-clusterrolebinding so that we actually test the RBAC set up in the helm chart
	err := run(ctx, "kubectl", "delete", "clusterrolebinding", "telepresence-clusterrolebinding")
	hs.NoError(err)

	hs.NoError(hs.helmInstall(ctx, hs.managerNamespace1, hs.appNamespace1))

	hs.Eventually(func() bool {
		return run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer") == nil
	}, 10*time.Second, time.Second)
}

func (hs *helmSuite) TestA_CanConnect() {
	ctx := dlog.NewTestContext(hs.T(), false)
	stdout, stderr := telepresenceContext(ctx, "connect")
	hs.Empty(stderr)
	hs.Contains(stdout, "Connected to context")
	// This has been shamelessly lifted from connectedSuite.TestC_ProxiesOutboundTraffic
	// TODO: There might be a better way to re-use this logic.
	for i := 0; i < serviceCount; i++ {
		svc := fmt.Sprintf("hello-%d.%s", i, hs.appNamespace1)
		expectedOutput := fmt.Sprintf("Request served by hello-%d", i)
		hs.Require().Eventually(
			// condition
			func() bool {
				dlog.Infof(ctx, "trying %q...", "http://"+svc)
				hc := http.Client{Timeout: time.Second}
				resp, err := hc.Get("http://" + svc)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				defer resp.Body.Close()
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				dlog.Infof(ctx, "body: %q", body)
				return strings.Contains(string(body), expectedOutput)
			},
			15*time.Second, // waitfor
			3*time.Second,  // polling interval
			`body of %q contains %q`, "http://"+svc, expectedOutput,
		)
	}
}

func (hs *helmSuite) TestB_CanInterceptInManagedNamespace() {
	// Lifted from TestF_SuccessfullyInterceptsDeploymentWithProbes
	defer telepresence(hs.T(), "leave", "with-probes-"+hs.appNamespace1)

	stdout, stderr := telepresence(hs.T(), "intercept", "--namespace", hs.appNamespace1, "--mount", "false", "with-probes", "--port", "9090")
	hs.Empty(stderr)
	hs.Contains(stdout, "Using Deployment with-probes")
	stdout, stderr = telepresence(hs.T(), "list", "--namespace", hs.appNamespace1, "--intercepts")
	hs.Empty(stderr)
	hs.Contains(stdout, "with-probes: intercepted")
}

func (hs *helmSuite) TestC_CannotInterceptInUnmanagedNamespace() {
	ctx := dlog.NewTestContext(hs.T(), false)
	hs.tpSuite.namespace = hs.appNamespace2
	hs.NoError(hs.tpSuite.applyApp(ctx, "with-probes", "with-probes", 80))
	defer func() {
		hs.NoError(hs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "with-probes", "--context", "default"))
		hs.tpSuite.namespace = hs.appNamespace1
	}()
	_, stderr := telepresence(hs.T(), "intercept", "--namespace", hs.appNamespace2, "--mount", "false", "with-probes", "--port", "9090")
	hs.Contains(stderr, "Failed to establish intercept")
}

func (hs *helmSuite) TestD_WebhookInjectsInManagedNamespace() {
	ctx := dlog.NewTestContext(hs.T(), false)
	hs.NoError(hs.tpSuite.applyApp(ctx, "echo-auto-inject", "echo-auto-inject", 80))
	defer func() {
		hs.NoError(hs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "echo-auto-inject", "--context", "default"))
	}()

	hs.Eventually(func() bool {
		stdout, stderr := telepresence(hs.T(), "list", "--namespace", hs.appNamespace1, "--agents")
		hs.Empty(stderr)
		return strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		10*time.Second, // waitFor
		2*time.Second,  // polling interval
	)
}

func (hs *helmSuite) TestE_WebhookDoesntInjectInUnmanagedNamespace() {
	ctx := dlog.NewTestContext(hs.T(), false)
	defer func() {
		hs.NoError(hs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "echo-auto-inject", "--context", "default"))
		hs.tpSuite.namespace = hs.appNamespace1
	}()
	hs.tpSuite.namespace = hs.appNamespace2
	hs.NoError(hs.tpSuite.applyApp(ctx, "echo-auto-inject", "echo-auto-inject", 80))

	hs.Never(func() bool {
		stdout, stderr := telepresence(hs.T(), "list", "--namespace", hs.appNamespace2, "--agents")
		hs.Empty(stderr)
		return strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		10*time.Second, // waitFor
		2*time.Second,  // polling interval
	)
}

func (hs *helmSuite) TestF_MultipleInstalls() {
	ctx := dlog.NewTestContext(hs.T(), false)
	telepresenceContext(ctx, "quit")
	hs.tpSuite.namespace = hs.appNamespace2
	os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", hs.managerNamespace2)
	hs.NoError(hs.tpSuite.applyApp(ctx, "with-probes", "with-probes", 80))
	defer func() {
		hs.NoError(hs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "with-probes", "--context", "default"))
		hs.tpSuite.namespace = hs.appNamespace1
		os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", hs.managerNamespace1)
	}()

	hs.Run("Installs Successfully", func() {
		hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
		defer func() { hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer")) }()
		hs.NoError(hs.helmInstall(ctx, hs.managerNamespace2, hs.appNamespace2))
	})
	hs.Run("Can be connected to", func() {
		configDir := hs.T().TempDir()
		// Needed to prevent a (harmless) message on stderr stating that there's no config to use
		ctx, err := setDefaultConfig(ctx, configDir)
		hs.NoError(err)
		stdout, stderr := telepresenceContext(ctx, "connect")
		hs.Empty(stderr)
		hs.Contains(stdout, "Connected to context")
		hs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", fmt.Sprintf("with-probes.%s", hs.appNamespace2)) == nil
		}, 7*time.Second, 1*time.Second)
	})
	hs.Run("Can intercept", func() {
		defer telepresence(hs.T(), "leave", "with-probes-"+hs.appNamespace2)

		stdout, stderr := telepresence(hs.T(), "intercept", "--namespace", hs.appNamespace2, "--mount", "false", "with-probes", "--port", "9090")
		hs.Empty(stderr)
		hs.Contains(stdout, "Using Deployment with-probes")
		stdout, stderr = telepresence(hs.T(), "list", "--namespace", hs.appNamespace2, "--intercepts")
		hs.Empty(stderr)
		hs.Contains(stdout, "with-probes: intercepted")
	})
	hs.Run("Uninstalls successfully", func() {
		hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
		defer func() { hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer")) }()
		hs.NoError(run(ctx, "helm", "uninstall", "traffic-manager", "-n", hs.managerNamespace2))
	})
}

func (hs *helmSuite) TestG_CollidingInstalls() {
	ctx := dlog.NewTestContext(hs.T(), false)
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	defer func() { hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer")) }()
	hs.Error(hs.helmInstall(ctx, hs.managerNamespace2, hs.appNamespace1, hs.appNamespace2))
}

func (hs *helmSuite) TestZ_Uninstall() {
	ctx := dlog.NewTestContext(hs.T(), false)
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	hs.NoError(run(ctx, "helm", "uninstall", "traffic-manager", "-n", hs.managerNamespace1))
	// Make sure the RBAC was cleaned up by uninstall
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer"))
	hs.Error(run(ctx, "kubectl", "get", "namespaces"))
	hs.Error(run(ctx, "kubectl", "get", "deploy", "-n", hs.managerNamespace1))
}

func (hs *helmSuite) helmInstall(ctx context.Context, managerNamespace string, appNamespaces ...string) error {
	clusterID, err := hs.tpSuite.kubectlOut(ctx, "get", "ns", hs.appNamespace1, "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return err
	}
	helmValues := "pkg/client/cli/testdata/test-values.yaml"
	helmChart := "charts/telepresence"
	return run(ctx, "helm", "install", "traffic-manager",
		"-n", managerNamespace, helmChart,
		"--set", fmt.Sprintf("clusterId=%s", clusterID),
		"--set", fmt.Sprintf("image.registry=%s", dtest.DockerRegistry(ctx)),
		"--set", fmt.Sprintf("image.tag=%s", hs.tpSuite.testVersion[1:]),
		"--set", fmt.Sprintf("clientRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		"--set", fmt.Sprintf("managerRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		"-f", helmValues,
	)
}

func (hs *helmSuite) TearDownSuite() {
	ctx := dlog.NewTestContext(hs.T(), false)
	// Restore the rbac we blew up in the setup
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	hs.NoError(run(ctx, "kubectl", "apply", "-f", "k8s/client_rbac.yaml"))
	hs.NoError(run(ctx, "kubectl", "delete", "namespace", hs.appNamespace2, hs.managerNamespace2))
}

func (ts *telepresenceSuite) applyApp(c context.Context, name, svcName string, port int) error {
	err := ts.kubectl(c, "apply", "-f", fmt.Sprintf("k8s/%s.yaml", name), "--context", "default")
	if err != nil {
		return fmt.Errorf("failed to deploy %s: %w", name, err)
	}
	return ts.waitForService(c, svcName, port)
}

func (ts *telepresenceSuite) applyEchoService(c context.Context, name string) error {
	err := ts.kubectl(c, "create", "deploy", name, "--image", "jmalloc/echo-server:0.1.0")
	if err != nil {
		return fmt.Errorf("failed to create deployment %s: %w", name, err)
	}
	err = ts.kubectl(c, "expose", "deploy", name, "--port", "80", "--target-port", "8080")
	if err != nil {
		return fmt.Errorf("failed to expose deployment %s: %w", name, err)
	}
	return ts.waitForService(c, name, 80)
}

func (ts *telepresenceSuite) waitForService(c context.Context, name string, port int) error {
	c, cancel := context.WithTimeout(c, 120*time.Second)
	defer cancel()

	// Since this function can be called multiple times in parallel
	// we add the name of the service to the title of the pod so they
	// can run at the same time. We strip out any characters that we
	// can't use in a name in k8s.
	reg := regexp.MustCompile("[^a-zA-Z0-9-]+")
	k8sSafeName := reg.ReplaceAllString(name, "")
	containerName := fmt.Sprintf("curl-%s-from-cluster", k8sSafeName)
	for c.Err() == nil {
		time.Sleep(time.Second)
		err := ts.kubectl(c, "run", containerName, "--context", "default", "--rm", "-it",
			"--image=docker.io/pstauffer/curl", "--restart=Never", "--",
			"curl", "--silent", "--output", "/dev/null",
			fmt.Sprintf("http://%s.%s:%d", name, ts.namespace, port),
		)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for %s service", name)
}

func (ts *telepresenceSuite) kubectl(c context.Context, args ...string) error {
	return run(c, append([]string{"kubectl", "--namespace", ts.namespace}, args...)...)
}

func (ts *telepresenceSuite) kubectlOut(ctx context.Context, args ...string) (string, error) {
	return output(ctx, append([]string{"kubectl", "--namespace", ts.namespace}, args...)...)
}

func (ts *telepresenceSuite) publishManager() error {
	ctx := dlog.NewTestContext(ts.T(), true)
	cmd := dexec.CommandContext(ctx, "make", "push-image")

	// Go sets a lot of variables that we don't want to pass on to the ko executable. If we do,
	// then it builds for the platform indicated by those variables.
	cmd.Env = []string{
		"TELEPRESENCE_VERSION=" + ts.testVersion,
		"TELEPRESENCE_REGISTRY=" + dtest.DockerRegistry(ctx),
	}
	includeEnv := []string{"KO_DOCKER_REPO=", "HOME=", "PATH=", "LOGNAME=", "TMPDIR=", "MAKELEVEL="}
	for _, env := range os.Environ() {
		for _, incl := range includeEnv {
			if strings.HasPrefix(env, incl) {
				cmd.Env = append(cmd.Env, env)
				break
			}
		}
	}
	if err := cmd.Run(); err != nil {
		return client.RunError(err)
	}
	return nil
}

func (ts *telepresenceSuite) buildExecutable(c context.Context) (string, error) {
	executable := filepath.Join("build-output", "bin", "/telepresence")
	return executable, run(c, "go", "build", "-ldflags",
		fmt.Sprintf("-X=github.com/telepresenceio/telepresence/v2/pkg/version.Version=%s", ts.testVersion),
		"-o", executable, "./cmd/telepresence")
}

func (ts *telepresenceSuite) setupKubeConfig(ctx context.Context) {
	kubeconfig := dtest.Kubeconfig(ctx)
	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KUBECONFIG", kubeconfig)
	err := run(ctx, "kubectl", "create", "namespace", ts.namespace)
	ts.NoError(err)
	err = run(ctx, "kubectl", "apply", "-f", "k8s/client_rbac.yaml")
	ts.NoError(err)

	// This is how we create a user that has their rbac restricted to what we have in
	// k8s/client_rbac.yaml. We do this by creating a service account and then getting
	// the token from said service account and storing it in our kubeconfig.
	secret, err := output(ctx, "kubectl", "get", "sa", "telepresence-test-developer", "-o", "jsonpath={.secrets[0].name}")
	ts.NoError(err)
	encSecret, err := output(ctx, "kubectl", "get", "secret", secret, "-o", "jsonpath={.data.token}")
	ts.NoError(err)
	token, err := base64.StdEncoding.DecodeString(encSecret)
	ts.NoError(err)
	err = run(ctx, "kubectl", "config", "set-credentials", "telepresence-test-developer", "--token", string(token))
	ts.NoError(err)
	err = run(ctx, "kubectl", "config", "set-context", "telepresence-test-developer", "--user", "telepresence-test-developer", "--cluster", "default")
	ts.NoError(err)

	// We start with the default context, and will switch to the
	// telepresence-test-developer user later in the tests
	err = run(ctx, "kubectl", "config", "use-context", "default")
	ts.NoError(err)
}

func run(c context.Context, args ...string) error {
	return client.RunError(dexec.CommandContext(c, args[0], args[1:]...).Run())
}

func output(ctx context.Context, args ...string) (string, error) {
	cmd := dexec.CommandContext(ctx, args[0], args[1:]...)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	return string(out), client.RunError(err)
}

// telepresence executes the CLI command in-process
func telepresence(t testing.TB, args ...string) (string, string) {
	ctx := dlog.NewTestContext(t, false)

	// Ensure the config.yml is using the dtest registry by default
	configDir := t.TempDir()
	ctx, err := setDefaultConfig(ctx, configDir)
	if err != nil {
		t.Error(err)
	}

	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	return telepresenceContext(ctx, args...)
}

// telepresence executes the CLI command in-process
func telepresenceContext(ctx context.Context, args ...string) (string, string) {
	var stdout, stderr strings.Builder

	configDir, _ := filelocation.AppUserConfigDir(ctx)
	logDir, _ := filelocation.AppUserLogDir(ctx)

	cmd := dexec.CommandContext(ctx, client.GetExe(), args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"DEV_TELEPRESENCE_CONFIG_DIR="+configDir,
		"DEV_TELEPRESENCE_LOG_DIR="+logDir)

	_ = cmd.Run()

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())
}

// setDefaultConfig creates a config that has the registry set correctly.
// This ensures that the config on the machine of whatever is running the test,
// isn't used, which could cause conflict with the tests.
func setDefaultConfig(ctx context.Context, configDir string) (context.Context, error) {
	registry := dtest.DockerRegistry(ctx)
	configYml := fmt.Sprintf("images:\n  registry: %s\n  webhookRegistry: %s\n", registry, registry)
	return setConfig(ctx, configDir, configYml)
}

// setConfig clears the config and creates one from the configYml provided. Use this
// if you are testing components of the config.yml, otherwise you can use setDefaultConfig.
func setConfig(ctx context.Context, configDir, configYml string) (context.Context, error) {
	client.ResetConfig(ctx)
	config, err := os.Create(filepath.Join(configDir, "config.yml"))
	if err != nil {
		return ctx, err
	}

	_, err = config.WriteString(configYml)
	if err != nil {
		return ctx, err
	}
	config.Close()

	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	return ctx, nil
}
