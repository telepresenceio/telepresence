package cli_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	_ "github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	_ "github.com/telepresenceio/telepresence/v2/pkg/client/connector"
	_ "github.com/telepresenceio/telepresence/v2/pkg/client/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// serviceCount is the number of interceptable services that gets installed
// in the cluster and later intercepted
const serviceCount = 4

func TestTelepresence(t *testing.T) {
	ctx := testContext(t)
	dtest.WithMachineLock(ctx, func(ctx context.Context) {
		suite.Run(t, new(telepresenceSuite))
	})
}

type telepresenceSuite struct {
	suite.Suite
	testVersion          string
	namespace            string
	managerTestNamespace string
	logCapturingPods     sync.Map
}

func (ts *telepresenceSuite) SetupSuite() {
	require := ts.Require()
	_ = os.Chdir("../../..")

	// Remove very verbose output from DTEST initialization
	log.SetOutput(io.Discard)

	suffix, isCi := os.LookupEnv("CIRCLE_SHA1")
	if !isCi {
		suffix = strconv.Itoa(os.Getpid())
	}
	ts.testVersion = fmt.Sprintf("v2.0.0-gotest.%s", suffix)
	ts.namespace = fmt.Sprintf("telepresence-%s", suffix)
	ts.managerTestNamespace = fmt.Sprintf("ambassador-%s", suffix)

	version.Version = ts.testVersion

	ctx := testContext(ts.T())

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		executable, err := ts.buildExecutable(ctx)
		ts.NoError(err)
		client.SetExe(executable)
	}()

	_ = os.Remove(client.ConnectorSocketName)

	var err error
	if goRuntime.GOOS != "windows" {
		err := run(ctx, "sudo", "true")
		require.NoError(err, "acquire privileges")
	}
	os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", ts.managerTestNamespace)
	os.Setenv("DTEST_REGISTRY", dtest.DockerRegistry(ctx)) // Prevent extra calls to dtest.RegistryUp() which may panic

	if !isCi {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ts.publishManager()
			ts.NoError(err)
		}()
	}

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
		err = ts.applyApp(ctx, "echo-headless", "echo-headless", 8080)
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
	ctx := testContext(ts.T())
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
			ts.Contains(stdout, "Launching Telepresence Root Daemon")
			ts.Contains(stdout, "Launching Telepresence User Daemon")
			ts.Contains(stdout, "Telepresence Root Daemon quitting... done")
		})
	})

	ts.Run("Connect with non existing context", func() {
		ts.Run("Reports connect error and exits", func() {
			stdout, stderr := telepresence(ts.T(), "connect", "--context", "not-likely-to-exist")
			ts.Contains(stderr, `"not-likely-to-exist" does not exist`)
			ts.Contains(stdout, "Launching Telepresence Root Daemon")
			ts.Contains(stdout, "Launching Telepresence User Daemon")
			ts.Contains(stdout, "Telepresence Root Daemon quitting... done")
		})
	})

	ts.Run("Connect with a command", func() {
		ts.Run("Connects, executes the command, and then exits", func() {
			stdout, stderr := telepresence(ts.T(), "connect", "--", client.GetExe(), "status")
			require := ts.Require()
			require.Empty(stderr)
			require.Contains(stdout, "Launching Telepresence Root Daemon")
			require.Contains(stdout, "Launching Telepresence User Daemon")
			require.Contains(stdout, "Connected to context")
			require.Contains(stdout, "Kubernetes context:")
			require.Regexp(`Telepresence proxy:\s+ON`, stdout)
			require.Contains(stdout, "Telepresence Root Daemon quitting... done")
			ts.NoError(ts.capturePodLogs(testContext(ts.T()), "traffic-manager", ts.managerTestNamespace))
		})
	})

	ts.Run("Root Daemon Log Level", func() {
		t := ts.T()
		require := ts.Require()
		ctx := testContext(t)

		logDir := t.TempDir()
		ctx = filelocation.WithAppUserLogDir(ctx, logDir)
		_, stderr := telepresenceContext(ctx, "connect")
		require.Empty(stderr)
		_, stderr = telepresenceContext(ctx, "quit")
		require.Empty(stderr)

		rootLogName := filepath.Join(logDir, "daemon.log")
		rootLog, err := os.Open(rootLogName)
		require.NoError(err)
		defer rootLog.Close()

		hasDebug := false
		scn := bufio.NewScanner(rootLog)
		match := regexp.MustCompile(` debug +daemon/server`)
		for scn.Scan() && !hasDebug {
			hasDebug = match.MatchString(scn.Text())
		}
		ts.True(hasDebug, "daemon.log does not contain expected debug statements")

		// Give daemon time to really quit and release the daemon.log to avoid a "TempDir RemoveAll cleanup" failure
		// with an "Access is denied" error.
		time.Sleep(time.Second)
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

		ctx := testContext(t)
		defer os.Setenv("KUBECONFIG", origKubeconfigFileName)
		os.Setenv("KUBECONFIG", kubeconfigFileName)
		ctx = filelocation.WithAppUserLogDir(ctx, tmpDir)

		logDir, err := filelocation.AppUserLogDir(ctx)
		logFile := filepath.Join(logDir, "daemon.log")
		require.NoError(err)

		_, stderr := telepresenceContext(ctx, "connect")
		require.Empty(stderr)
		defer func() {
			_, stderr = telepresenceContext(ctx, "quit")
			require.Empty(stderr)
		}()

		retryCount := 0
		ts.Eventually(func() bool {
			// Test with ".org" suffix that was added as an include-suffix
			host := fmt.Sprintf("zwslkjsdf-%d.org", retryCount)
			short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			_ = run(short, "curl", "--silent", "--connect-timeout", "0.01", host)

			// Give query time to reach telepresence and produce a log entry
			dtime.SleepWithContext(ctx, 10*time.Millisecond)

			rootLog, err := os.Open(logFile)
			require.NoError(err)
			defer rootLog.Close()

			scanFor := fmt.Sprintf(`LookupHost "%s"`, host)
			scn := bufio.NewScanner(rootLog)
			for scn.Scan() {
				if strings.Contains(scn.Text(), scanFor) {
					return true
				}
			}
			retryCount++
			return false
		}, 10*time.Second, time.Second, "daemon.log does not contain expected LookupHost entry")
	})

	ts.Run("Webhook Agent Image From Config", func() {
		t := ts.T()
		require := ts.Require()
		uninstallTrafficManager := func() {
			stdout, stderr := telepresence(t, "uninstall", "--everything")
			require.Empty(stderr)
			require.Contains(stdout, "Root Daemon quitting... done")
		}
		// Remove the traffic-manager since we are altering config that applies to
		// creating the traffic-manager
		uninstallTrafficManager()
		stdout, stderr := telepresence(t, "uninstall", "--everything")
		require.Empty(stderr)
		require.Contains(stdout, "Root Daemon quitting... done")

		// Use a config with agentImage and webhookAgentImage to validate that it's the
		// latter that is used in the traffic-manager
		ctx := testContextWithConfig(t, &client.Config{
			Images: client.Images{
				AgentImage:        "notUsed:0.0.1",
				WebhookAgentImage: "imageFromConfig:0.0.1",
			},
		})
		registry := dtest.DockerRegistry(ctx)
		_, stderr = telepresenceContext(ctx, "connect")
		require.Empty(stderr)

		// When this function ends we uninstall the manager
		defer func() {
			uninstallTrafficManager()
		}()

		image, err := ts.kubectlOut(ctx, "get",
			"--namespace", ts.managerTestNamespace,
			"deploy", "traffic-manager",
			"--ignore-not-found",
			"-o",
			"jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TELEPRESENCE_AGENT_IMAGE')].value}")
		require.NoError(err)
		actualRegistry, err := ts.kubectlOut(ctx, "get",
			"--namespace", ts.managerTestNamespace,
			"deploy", "traffic-manager",
			"--ignore-not-found",
			"-o",
			"jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TELEPRESENCE_REGISTRY')].value}")
		require.NoError(err)
		ts.Equal("imageFromConfig:0.0.1", image)
		ts.Equal(registry, actualRegistry)
		ts.NoError(ts.capturePodLogs(ctx, "traffic-manager", ts.managerTestNamespace))
	})
}

func (ts *telepresenceSuite) TestB_Connected() {
	suite.Run(ts.T(), &connectedSuite{tpSuite: ts})
}

func (ts *telepresenceSuite) TestC_Uninstall() {
	// telepresence(ts.T(), "connect")

	ts.Run("Uninstalls the traffic manager and quits", func() {
		require := ts.Require()
		ctx := testContext(ts.T())

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
		ts.NoError(ts.capturePodLogs(ctx, "traffic-manager", ts.managerTestNamespace))
		require.NoError(run(ctx, "kubectl", "config", "use-context", "default"))

		// Add webhook agent to test webhook uninstall
		jobname := "echo-auto-inject"
		deployname := "deploy/" + jobname
		require.NoError(ts.applyApp(ctx, jobname, jobname, 80))

		defer func() {
			require.NoError(ts.kubectl(ctx, "delete", "svc,deploy", jobname))
		}()

		require.NoError(ts.rolloutStatusWait(ctx, deployname, ts.namespace))
		stdout, stderr := telepresence(ts.T(), "list", "--namespace", ts.namespace, "--agents")
		require.Empty(stderr)
		require.Contains(stdout, jobname+": ready to intercept (traffic-agent already installed)")

		// The telepresence-test-developer will not be able to uninstall everything
		stdout, stderr = telepresence(ts.T(), "uninstall", "--everything")
		require.Empty(stderr)
		require.Contains(stdout, "Root Daemon quitting... done")

		// Double check webhook agent is uninstalled
		require.NoError(ts.rolloutStatusWait(ctx, deployname, ts.namespace))
		ts.Eventually(func() bool {
			stdout, err = ts.kubectlOut(ctx, "get", "pods", "-n", ts.namespace)
			if err != nil {
				return false
			}
			match, err := regexp.MatchString(jobname+`-[a-z0-9]+-[a-z0-9]+\s+1/1\s+Running`, stdout)
			return err == nil && match
		}, 10*time.Second, 2*time.Second)

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
	c := testContextWithConfig(cs.T(), &client.Config{
		Images: client.Images{WebhookAgentImage: "tel2:" + cs.tpSuite.testVersion[1:]},
	})

	// Uninstall and re-install to make sure the traffic-manager is installed with the right config
	_, stderr := telepresenceContext(c, "uninstall", "-e")
	require.Empty(stderr)

	// Deployment with webhook annotation installed before telepresence so that agent is absent
	jobname := "echo-auto-inject"
	require.NoError(cs.tpSuite.applyApp(c, jobname, jobname, 80))

	// Connect + quit before we change contexts to ensure the
	// traffic-manager is installed
	_, stderr = telepresenceContext(c, "connect")
	require.Empty(stderr)

	// Verify webhook agent is installed on intercept
	stdout, stderr := telepresence(cs.T(), "intercept", jobname, "--namespace", cs.ns())
	require.Empty(stderr)
	regex := regexp.MustCompile("Intercept name    : (.*?)\n")
	interceptName := regex.FindStringSubmatch(stdout)
	require.NotNil(interceptName)
	_, stderr = telepresence(cs.T(), "leave", interceptName[1])
	require.Empty(stderr)
	require.NoError(cs.tpSuite.kubectl(c, "delete", "svc,deploy", jobname))

	time.Sleep(time.Second) // Allow some time before we quit
	_, stderr = telepresenceContext(c, "quit")
	require.Empty(stderr)
	require.NoError(cs.tpSuite.capturePodLogs(c, "traffic-manager", cs.tpSuite.managerTestNamespace))

	cs.Eventually(func() bool {
		return run(c, "kubectl", "config", "use-context", "telepresence-test-developer") == nil
	}, 10*time.Second, time.Second)

	stdout, stderr = telepresenceContext(c, "connect")
	require.Empty(stderr)
	require.Contains(stdout, "Connected to context")

	// Give outbound interceptor 15 seconds to kick in.
	require.Eventually(
		// condition
		func() bool {
			stdout, _ := telepresenceContext(c, "status")
			return regexp.MustCompile(`Telepresence proxy:\s+ON`).FindString(stdout) != ""
		},
		15*time.Second, // waitFor
		time.Second,    // polling interval
		"Timeout waiting for network overrides to establish", // msg
	)
	require.NoError(cs.tpSuite.capturePodLogs(c, "traffic-manager", cs.tpSuite.managerTestNamespace))
}

func (cs *connectedSuite) TearDownSuite() {
	stdout, stderr := telepresence(cs.T(), "quit")
	cs.Empty(stderr)
	cs.Contains(stdout, "quitting")
	c := testContext(cs.T())
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
	ctx := testContext(cs.T())
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
				body, err := io.ReadAll(resp.Body)
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

func (cs *connectedSuite) TestD_GetClusterID() {
	c := testContext(cs.T())
	c, cancel := context.WithTimeout(c, 15*time.Second)
	defer cancel()
	trafficManagerSvc := fmt.Sprintf("traffic-manager.%s:8081", cs.tpSuite.managerTestNamespace)
	conn, err := grpc.DialContext(c, trafficManagerSvc, grpc.WithInsecure(), grpc.WithBlock())
	cs.NoError(err, fmt.Sprintf("error connecting to %s: %s", trafficManagerSvc, err))
	defer conn.Close()

	mgr := manager.NewManagerClient(conn)
	license, err := mgr.GetLicense(c, &empty.Empty{})
	cs.NoError(err, "error with GetLicense gRPC call")
	cs.NotEmpty(license.ErrMsg, "there should be an error message since there's no license installed")

	// Get clusterID from the cluster
	clusterID, err := output(c, "kubectl", "get", "ns", "default", "-o", "jsonpath={.metadata.uid}")
	cs.NoError(err, "error getting cluster-id with kubectl")
	cs.Equal(clusterID, license.GetClusterId(), fmt.Sprintf("license from cluster %s does not match from getLicense %s, %#v", clusterID, license.GetClusterId(), license))
}

func (cs *connectedSuite) TestD_Intercepted() {
	suite.Run(cs.T(), &interceptedSuite{tpSuite: cs.tpSuite})
}

func (cs *connectedSuite) TestE_PodWithSubdomain() {
	require := cs.Require()
	c := testContext(cs.T())
	require.NoError(cs.tpSuite.applyApp(c, "echo-w-subdomain", "echo.subsonic", 8080))
	defer func() {
		cs.NoError(cs.tpSuite.kubectl(c, "delete", "svc", "subsonic", "--context", "default"))
		cs.NoError(cs.tpSuite.kubectl(c, "delete", "deploy", "echo-subsonic", "--context", "default"))
	}()

	dlog.Infof(c, "Trying to resolve in namespace %s", cs.ns())
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
	require.NotContains(stdout, "Volume Mount Point")
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
	require.NotContains(stdout, "Volume Mount Point")
}

func (cs *connectedSuite) TestH_SuccessfullyInterceptsStatefulSet() {
	defer telepresence(cs.T(), "leave", "ss-echo-"+cs.ns())

	require := cs.Require()
	stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", "ss-echo", "--port", "9092")
	require.Empty(stderr)
	require.Contains(stdout, "Using StatefulSet ss-echo")
	stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
	require.Empty(stderr)
	require.Contains(stdout, "ss-echo: intercepted")
	require.NotContains(stdout, "Volume Mount Point")
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
		ctx := testContext(cs.T())

		// service can be resolve with unqualified name
		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "ss-echo") == nil
		}, 30*time.Second, 3*time.Second)
	})

	cs.Run("leaving renders services unavailable using unqualified name", func() {
		stdout, stderr := telepresence(cs.T(), "leave", "mylocal")
		cs.Empty(stdout)
		cs.Empty(stderr)
		ctx := testContext(cs.T())
		cs.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			return run(ctx, "curl", "--silent", "ss-echo") != nil
		}, 30*time.Second, 3*time.Second)
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
	// This test only runs on linux as it requires docker, and CI can't run linux docker containers inside non-linux runners
	if goRuntime.GOOS != "linux" {
		cs.T().SkipNow()
	}
	require := cs.Require()
	ctx := testContext(cs.T())

	svc := "hello-0"
	tag := "telepresence/hello-test"
	testDir := "pkg/client/cli/testdata/hello"
	_, err := output(ctx, "docker", "build", "-t", tag, testDir)
	require.NoError(err)
	abs, err := filepath.Abs(testDir)
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
		// Response contains env variables TELEPRESENCE_CONTAINER and TELEPRESENCE_INTERCEPT_ID
		expectedOutput := regexp.MustCompile(`Hello from intercepted echo-server with id [0-9a-f-]+:` + svc)
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
				return expectedOutput.MatchString(out)
			},
			30*time.Second, // waitFor
			1*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
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
	cs.Eventually(func() bool {
		stdout, stderr := telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
		if stderr != "" {
			return false
		}
		return strings.Contains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
	},
		10*time.Second,
		1*time.Second,
	)
}

func (cs *connectedSuite) TestM_AutoInjectedAgent() {
	ctx := testContext(cs.T())
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
			20*time.Second, // waitFor
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
		ctx := testContext(cs.T())

		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "localhost:8081") == nil
		}, 15*time.Second, 2*time.Second)

		cs.Eventually(func() bool {
			return run(ctx, "curl", "--silent", "localhost:8082") == nil
		}, 15*time.Second, 2*time.Second)
	})

	cs.Run("Non-forwarded port is not reachable", func() {
		ctx := testContext(cs.T())

		cs.Eventually(func() bool {
			ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			return run(ctx, "curl", "--silent", "localhost:8083") != nil
		}, 15*time.Second, 2*time.Second)
	})
}

func (cs *connectedSuite) TestO_LargeRequest() {
	require := cs.Require()
	client := &http.Client{Timeout: 30 * time.Second}
	b := make([]byte, 1024*1024*5)
	b[0] = '!'
	b[1] = '\n'
	for i := range b[2:] {
		b[i+2] = 'A'
	}
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://hello-0.%s/put", cs.ns()), bytes.NewBuffer(b))
	require.NoError(err)

	resp, err := client.Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(resp.StatusCode, 200)

	buf := make([]byte, 1)
	_, err = resp.Body.Read(buf)
	for err == nil && buf[0] != '!' {
		_, err = resp.Body.Read(buf)
	}
	require.NoError(err)
	_, err = resp.Body.Read(buf)
	require.NoError(err)

	buf = make([]byte, len(b)-2)
	i := 0
	for err == nil {
		var j int
		j, err = resp.Body.Read(buf[i:])
		i += j
	}

	require.Equal(io.EOF, err)
	cs.Equal(len(buf), i)
	// Do this instead of cs.Equal(b[2:], buf) so that on failure we don't print two 5MB buffers to the terminal
	cs.Equal(0, bytes.Compare(b[2:], buf))
}

func (cs *connectedSuite) TestP_SuccessfullyInterceptsHeadlessService() {
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(dlog.NewTestContext(cs.T(), true)))
	defer cancel()
	services := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	const svc = "echo-headless"
	services.Go("intercept", func(ctx context.Context) error {
		sc := &dhttp.ServerConfig{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "%s from intercept at %s", svc, r.URL.Path)
			}),
		}
		return sc.ListenAndServe(ctx, ":9092")
	})
	require := cs.Require()
	for _, test := range []struct {
		webhook bool
		name    string
	}{
		{
			webhook: true,
			name:    "injected from webhook",
		},
		{
			webhook: false,
			name:    "injected from command",
		},
	} {
		cs.Run(test.name, func() {
			if test.webhook {
				require.NoError(annotateForWebhook(ctx, "statefulset", "echo-headless", cs.tpSuite.namespace, 8080))
			}
			stdout, stderr := telepresence(cs.T(), "intercept", "--namespace", cs.ns(), "--mount", "false", svc, "--port", "9092")
			require.Empty(stderr)
			require.Contains(stdout, "Using StatefulSet echo-headless")

			defer func() {
				telepresence(cs.T(), "leave", "echo-headless-"+cs.ns())
				if test.webhook {
					require.NoError(dropWebhookAnnotation(ctx, "statefulset", "echo-headless", cs.tpSuite.namespace))
				} else {
					telepresence(cs.T(), "uninstall", "--agent", "echo-headless", "-n", cs.tpSuite.namespace)
				}
			}()

			stdout, stderr = telepresence(cs.T(), "list", "--namespace", cs.ns(), "--intercepts")
			require.Empty(stderr)
			require.Contains(stdout, "echo-headless: intercepted")
			require.NotContains(stdout, "Volume Mount Point")

			expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
			cs.Require().Eventually(
				// condition
				func() bool {
					ip, err := net.DefaultResolver.LookupHost(ctx, svc)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					if 1 != len(ip) {
						dlog.Infof(ctx, "Lookup for %s returned %v", svc, ip)
						return false
					}

					url := fmt.Sprintf("http://%s:8080", svc)

					dlog.Infof(ctx, "trying %q...", url)
					hc := http.Client{Timeout: 2 * time.Second}
					resp, err := hc.Get(url)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					defer resp.Body.Close()
					dlog.Infof(ctx, "status code: %v", resp.StatusCode)
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					dlog.Infof(ctx, "body: %q", body)
					return string(body) == expectedOutput
				},
				time.Minute,   // waitFor
				3*time.Second, // polling interval
				`body of %q equals %q`, "http://"+svc, expectedOutput,
			)
		})
	}
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
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(testContext(is.T())))
	is.services = dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	is.cancelServices = cancel

	err := annotateForWebhook(ctx, "deploy", "hello-3", is.tpSuite.namespace, 80)
	is.NoError(err)

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
	// TempDir() will not be a valid mount on windows -- it wants a lettered drive.
	if goRuntime.GOOS == "windows" {
		is.mountPoint = "T:"
	}
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
	ctx := testContext(is.T())
	err := dropWebhookAnnotation(ctx, "deploy", "hello-3", is.tpSuite.namespace)
	is.NoError(err)
	is.NoError(is.tpSuite.rolloutStatusWait(ctx, "deploy/hello-3", is.tpSuite.namespace))

	is.cancelServices()
	is.NoError(is.services.Wait())
	time.Sleep(time.Second) // Allow some time for processes to die and intercepts to vanish
}

func (is *interceptedSuite) TestA_VerifyingResponsesFromInterceptor() {
	ctx := testContext(is.T())
	for i := 0; i < serviceCount; i++ {
		svc := fmt.Sprintf("hello-%d", i)
		is.Run("Test intercept "+svc, func() {
			expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
			is.Require().Eventually(
				// condition
				func() bool {
					ip, err := net.DefaultResolver.LookupHost(ctx, svc)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					if 1 != len(ip) {
						dlog.Infof(ctx, "Lookup for %s returned %v", svc, ip)
						return false
					}

					dlog.Infof(ctx, "trying %q...", "http://"+svc)
					hc := http.Client{Timeout: 2 * time.Second}
					resp, err := hc.Get("http://" + svc)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					defer resp.Body.Close()
					dlog.Infof(ctx, "status code: %v", resp.StatusCode)
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					dlog.Infof(ctx, "body: %q", body)
					return string(body) == expectedOutput
				},
				time.Minute,   // waitFor
				3*time.Second, // polling interval
				`body of %q equals %q`, "http://"+svc, expectedOutput,
			)
		})
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

// We do some tests in this suite that check that we only get logs from
// one agent, so we do these tests now since future tests remove agents
// and/or pods, which could potentially make those tests fail since we
// are only expecting one agent.
func (is *interceptedSuite) TestC_GatherLogs() {
	require := is.Require()
	outputDir := is.T().TempDir()

	getZipData := func(outputFile string) (bool, int, int, []string) {
		zipReader, err := zip.OpenReader(outputFile)
		require.NoError(err)
		// we collect and return the fileNames so that it makes it easier
		// to debug if tests fail
		var fileNames []string
		defer zipReader.Close()
		foundManager, foundAgents, yamlCount := false, 0, 0
		for _, f := range zipReader.File {
			fileNames = append(fileNames, f.Name)
			if strings.Contains(f.Name, "traffic-manager") {
				if strings.HasSuffix(f.Name, ".yaml") {
					yamlCount++
					continue
				}
				foundManager = true
				fileContent, err := cli.ReadZip(f)
				require.NoError(err)
				// We can be fairly certain we actually got a traffic-manager log
				// if we see the following
				require.Contains(string(fileContent), "Traffic Manager v2.")
			}
			if strings.Contains(f.Name, "hello-") {
				if strings.HasSuffix(f.Name, ".yaml") {
					yamlCount++
					continue
				}
				foundAgents++
				fileContent, err := cli.ReadZip(f)
				require.NoError(err)
				// We can be fairly certain we actually got a traffic-manager log
				// if we see the following
				require.Contains(string(fileContent), "Traffic Agent v2.")
			}
		}
		return foundManager, foundAgents, yamlCount, fileNames
	}
	is.Run("Get All Logs", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--get-pod-yaml", "--output-file", outputFile)
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.True(foundManager)
		require.Equal(serviceCount, foundAgents, fileNames)
		// One for each agent + one for the traffic manager
		require.Equal(serviceCount+1, yamlCount, fileNames)
	})

	is.Run("Get Manager Logs Only", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-agents=None")
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.True(foundManager)
		require.Equal(0, foundAgents, fileNames)
		require.Equal(1, yamlCount, fileNames)
	})
	is.Run("Get Agent Logs Only", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False")
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.False(foundManager)
		require.Equal(serviceCount, foundAgents, fileNames)
		require.Equal(serviceCount, yamlCount, fileNames)
	})
	is.Run("Get Only 1 Agent Log", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False", "--traffic-agents=hello-1")
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.False(foundManager)
		require.Equal(1, foundAgents, fileNames)
		require.Equal(1, yamlCount, fileNames)
	})
	is.Run("Don't get pod yaml if we aren't getting logs", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False", "--traffic-agents=None")
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.False(foundManager)
		require.Equal(0, foundAgents, fileNames)
		require.Equal(0, yamlCount, fileNames)
	})
	is.Run("No K8s Logs", func() {
		outputFile := fmt.Sprintf("%s/allLogs.zip", outputDir)
		_, stderr := telepresence(is.T(), "gather-logs", "--output-file", outputFile, "--traffic-manager=False", "--traffic-agents=None", "--get-pod-yaml")
		require.Empty(stderr)
		foundManager, foundAgents, yamlCount, fileNames := getZipData(outputFile)
		require.False(foundManager)
		require.Equal(0, foundAgents, fileNames)
		require.Equal(0, yamlCount, fileNames)
	})
}

func (is *interceptedSuite) TestD_MountedFilesystem() {
	require := is.Require()
	st, err := os.Stat(is.mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(is.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

func (is *interceptedSuite) TestE_RestartInterceptedPod() {
	ts := is.tpSuite
	assert := is.Assert()
	require := is.Require()
	c := testContext(is.T())
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: hello-0-` + is.ns() + `\s+State\s*: ([^\n]+)\n`))

	// Scale down to zero pods
	require.NoError(ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "0"))

	// Verify that intercept remains but that no agent is found. User require here
	// to avoid a hanging os.Stat call unless this succeeds.
	require.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			dlog.Infof(c, "Got match '%s'", match[1])
			return match[1] == "WAITING" || strings.Contains(match[1], `No agent found for "hello-0"`)
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify that volume mount is broken
	time.Sleep(time.Second) // avoid a stat just when the intercept became inactive as it sometimes causes a hang
	_, err := os.Stat(filepath.Join(is.mountPoint, "var"))
	assert.Error(err, "Stat on <mount point>/var succeeded although no agent was found")

	// Scale up again (start intercepted pod)
	assert.NoError(ts.kubectl(c, "--context", "default", "scale", "deploy", "hello-0", "--replicas", "1"))

	// Verify that intercept becomes active
	require.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify that volume mount is restored
	time.Sleep(time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	assert.Eventually(func() bool {
		st, err := os.Stat(filepath.Join(is.mountPoint, "var"))
		return err == nil && st.IsDir()
	}, 5*time.Second, time.Second)
}

func (is *interceptedSuite) TestF_StopInterceptedPodOfMany() {
	ts := is.tpSuite
	assert := is.Assert()
	require := is.Require()
	c := testContext(is.T())
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
		}, 15*time.Second, time.Second)

	// Verify that intercept is still active
	assert.Eventually(func() bool {
		stdout, _ := telepresence(is.T(), "--namespace", is.ns(), "list", "--intercepts")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify response from intercepting client
	require.Eventually(func() bool {
		hc := http.Client{Timeout: time.Second}
		resp, err := hc.Get("http://hello-0")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return "hello-0 from intercept at /" == string(body)
	}, 15*time.Second, time.Second)

	// Verify that volume mount is restored
	time.Sleep(time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	st, err := os.Stat(filepath.Join(is.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

func (is *interceptedSuite) TestG_ReportsPortConflict() {
	_, stderr := telepresence(is.T(), "intercept", "--namespace", is.ns(), "--port", "9001", "dummy-name")
	is.Contains(stderr, "Port 127.0.0.1:9001 is already in use by intercept hello-1-"+is.ns())
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
	ctx := testContext(hs.T())

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
	ctx := testContext(hs.T())
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
				body, err := io.ReadAll(resp.Body)
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
	ctx := testContext(hs.T())
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
	ctx := testContext(hs.T())
	hs.NoError(hs.tpSuite.applyApp(ctx, "echo-auto-inject", "echo-auto-inject", 80))
	defer func() {
		hs.NoError(hs.tpSuite.kubectl(ctx, "delete", "svc,deploy", "echo-auto-inject", "--context", "default"))
	}()

	hs.Eventually(func() bool {
		stdout, stderr := telepresence(hs.T(), "list", "--namespace", hs.appNamespace1, "--agents")
		hs.Empty(stderr)
		return strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		20*time.Second, // waitFor
		2*time.Second,  // polling interval
	)
}

func (hs *helmSuite) TestE_WebhookDoesntInjectInUnmanagedNamespace() {
	ctx := testContext(hs.T())
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
	ctx := testContext(hs.T())
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
		telepresence(hs.T(), "quit")
		hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
		defer func() { hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer")) }()
		hs.NoError(run(ctx, "helm", "uninstall", "traffic-manager", "-n", hs.managerNamespace2))
	})
}

func (hs *helmSuite) TestG_CollidingInstalls() {
	ctx := testContext(hs.T())
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	defer func() { hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer")) }()
	hs.Error(hs.helmInstall(ctx, hs.managerNamespace2, hs.appNamespace1, hs.appNamespace2))
}

func (hs *helmSuite) TestZ_Uninstall() {
	ctx := testContext(hs.T())
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "default"))
	telepresenceContext(ctx, "quit")
	hs.NoError(run(ctx, "helm", "uninstall", "traffic-manager", "-n", hs.managerNamespace1))
	// Make sure the RBAC was cleaned up by uninstall
	hs.NoError(run(ctx, "kubectl", "config", "use-context", "telepresence-test-developer"))
	// There seems to sometimes be a delay when rapidly changing contexts, so let's
	// ensure these commands use the correct context
	// TODO: if we stop seeing issues here, whenever we are using kubectl directly in these
	// tests that need a non-default context, we should do it manually and stop depending on
	// setting the context since it seems flakey.
	hs.Error(run(ctx, "kubectl", "get", "namespaces", "--context", "telepresence-test-developer"))
	hs.Error(run(ctx, "kubectl", "get", "deploy", "-n", hs.managerNamespace1, "--context", "telepresence-test-developer"))
}

func (hs *helmSuite) helmInstall(ctx context.Context, managerNamespace string, appNamespaces ...string) error {
	helmValues := "pkg/client/cli/testdata/test-values.yaml"
	helmChart := "charts/telepresence"
	err := run(ctx, "helm", "install", "traffic-manager",
		"-n", managerNamespace, helmChart,
		"--set", fmt.Sprintf("image.registry=%s", dtest.DockerRegistry(ctx)),
		"--set", fmt.Sprintf("image.tag=%s", hs.tpSuite.testVersion[1:]),
		"--set", fmt.Sprintf("clientRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		"--set", fmt.Sprintf("managerRbac.namespaces={%s}", strings.Join(append(appNamespaces, managerNamespace), ",")),
		// We don't want the tests or telepresence to depend on an extension host resolving, so we set it to localhost.
		"--set", "systemaHost=127.0.0.1",
		"-f", helmValues,
	)
	if err == nil {
		err = hs.tpSuite.capturePodLogs(ctx, "traffic-manager", managerNamespace)
	}
	return err
}

func (hs *helmSuite) TearDownSuite() {
	ctx := testContext(hs.T())
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
	c, cancel := context.WithTimeout(c, 3*time.Minute)
	defer cancel()

	// Since this function can be called multiple times in parallel
	// we add the name of the service to the title of the pod so they
	// can run at the same time. We strip out any characters that we
	// can't use in a name in k8s.
	reg := regexp.MustCompile("[^a-zA-Z0-9-]+")
	k8sSafeName := reg.ReplaceAllString(name, "")
	containerName := fmt.Sprintf("curl-%s-from-cluster", k8sSafeName)
	for c.Err() == nil {
		time.Sleep(3 * time.Second)
		err := ts.kubectl(c, "run", containerName, "--context", "default", "--rm", "-it",
			"--image=docker.io/pstauffer/curl", "--restart=Never", "--",
			"curl",
			fmt.Sprintf("http://%s.%s:%d", name, ts.namespace, port),
		)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for %s service", name)
}

func (ts *telepresenceSuite) rolloutStatusWait(ctx context.Context, workload, namespace string) error {
	if strings.HasPrefix(dtest.DockerRegistry(ctx), "localhost:") {
		// Assume that we run a local k3s setup and that we're affected by this bug: https://github.com/rancher/rancher/issues/21324
		return ts.kubectl(ctx, "wait", workload, "--for", "condition=available", "-n", namespace)
	}
	return ts.kubectl(ctx, "rollout", "status", "-w", workload, "-n", namespace)
}

func (ts *telepresenceSuite) kubectl(c context.Context, args ...string) error {
	return run(c, append([]string{"kubectl", "--namespace", ts.namespace}, args...)...)
}

func (ts *telepresenceSuite) kubectlOut(ctx context.Context, args ...string) (string, error) {
	return output(ctx, append([]string{"kubectl", "--namespace", ts.namespace}, args...)...)
}

func (ts *telepresenceSuite) publishManager() error {
	ctx := testContext(ts.T())
	cmd := dexec.CommandContext(ctx, "make", "push-image")
	if goRuntime.GOOS == "windows" {
		cmd = dexec.CommandContext(ctx, "winmake.bat", "push-image")
	}

	// Go sets a lot of variables that we don't want to pass on to the ko executable. If we do,
	// then it builds for the platform indicated by those variables.
	cmd.Env = []string{
		"TELEPRESENCE_VERSION=" + ts.testVersion,
		"TELEPRESENCE_REGISTRY=" + dtest.DockerRegistry(ctx),
	}
	includeEnv := []string{"HOME=", "PATH=", "Path=", "LOGNAME=", "TMPDIR=", "MAKELEVEL="}
	for _, env := range os.Environ() {
		for _, incl := range includeEnv {
			if strings.HasPrefix(env, incl) {
				dlog.Infof(ctx, "Setting variable %s to value %s", env, os.Getenv(env))
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
	err := run(c, "go", "run", "build-aux/package_embedded_chart/main.go", ts.testVersion)
	if err != nil {
		return "", fmt.Errorf("unable to build embedded helm chart: %w", err)
	}
	executable := filepath.Join("build-output", "bin", "/telepresence")
	if goRuntime.GOOS == "windows" {
		executable += ".exe"
	}
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

func (ts *telepresenceSuite) capturePodLogs(ctx context.Context, app, ns string) error {
	var pods string
	for i := 0; ; i++ {
		var err error
		pods, err = output(ctx, "kubectl", "-n", ns, "get", "pods", "-l", "app="+app, "-o", "jsonpath={.items[*].metadata.name}")
		if err != nil {
			return fmt.Errorf("failed to get %s pod in namespace %s: %w", app, ns, err)
		}
		pods = strings.TrimSpace(pods)
		if pods != "" || i == 5 {
			break
		}
		dtime.SleepWithContext(ctx, 2*time.Second)
	}
	if pods == "" {
		return fmt.Errorf("found no %s pods in namespace %s", app, ns)
	}

	// Let command die when the pod that it logs die
	ctx = dcontext.WithoutCancel(ctx)

	present := struct{}{}
	logDir, _ := filelocation.AppUserLogDir(ctx)
	for _, pod := range strings.Split(pods, " ") {
		if _, ok := ts.logCapturingPods.LoadOrStore(pod, present); ok {
			continue
		}
		logFile, err := os.Create(filepath.Join(logDir, pod+"-"+ns+".log"))
		if err != nil {
			ts.logCapturingPods.Delete(pod)
			return err
		}

		cmd := dexec.CommandContext(ctx, "kubectl", "-n", ns, "logs", "-f", pod)
		cmd.Stdout = logFile
		go func(pod string) {
			defer func() {
				_ = logFile.Close()
				ts.logCapturingPods.Delete(pod)
			}()
			if err := cmd.Run(); err != nil {
				dlog.Error(ctx, err)
			}
		}(pod)
	}
	return nil
}

func annotateForWebhook(ctx context.Context, objKind, objName, objNamespace string, servicePort int) error {
	err := run(ctx, "kubectl", "patch", "-n", objNamespace, objKind, objName, "-p", fmt.Sprintf(`
{
	"spec": {
		"template": {
			"metadata": {
				"annotations": {
					"telepresence.getambassador.io/inject-traffic-agent": "enabled",
					"telepresence.getambassador.io/inject-service-port": "%d"
				}
			}
		}
	}
}`, servicePort))
	if err != nil {
		return err
	}

	return run(ctx, "kubectl", "rollout", "status", "-w", fmt.Sprintf("%s/%s", objKind, objName), "-n", objNamespace)
}

func dropWebhookAnnotation(ctx context.Context, objKind, objName, objNamespace string) error {
	return run(ctx, "kubectl", "patch", "-n", objNamespace, objKind, objName, "--type=json", "-p", `[{
	"op": "remove",
	"path": "/spec/template/metadata/annotations/telepresence.getambassador.io~1inject-traffic-agent"
},
{
	"op": "remove",
	"path": "/spec/template/metadata/annotations/telepresence.getambassador.io~1inject-service-port"
}
]`)
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

// telepresence executes the CLI command
func telepresence(t testing.TB, args ...string) (string, string) {
	return telepresenceContext(testContext(t), args...)
}

// telepresence executes the CLI command in-process
func telepresenceContext(ctx context.Context, args ...string) (string, string) {
	var stdout, stderr strings.Builder
	// The cmd object does not log with timestamps, so dump out the telepresence command here.
	// That way we have a timestamped record of when it ran, which is useful for correlating with the daemon logs
	dlog.Debug(ctx, "telepresence invoked with", args)

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

func testContext(t testing.TB) context.Context {
	return testContextWithConfig(t, nil)
}

func testContextWithConfig(t testing.TB, addConfig *client.Config) context.Context {
	c := dlog.NewTestContext(t, false)
	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}
	c = client.WithEnv(c, env)

	config := client.GetDefaultConfig(c)
	config.LogLevels.UserDaemon = logrus.DebugLevel
	config.LogLevels.RootDaemon = logrus.DebugLevel

	to := &config.Timeouts
	to.PrivateAgentInstall = 240 * time.Second
	to.PrivateApply = 120 * time.Second
	to.PrivateClusterConnect = 60 * time.Second
	to.PrivateIntercept = 30 * time.Second
	to.PrivateProxyDial = 30 * time.Second
	to.PrivateTrafficManagerAPI = 60 * time.Second
	to.PrivateTrafficManagerConnect = 240 * time.Second
	to.PrivateHelm = 230 * time.Second

	registry := dtest.DockerRegistry(c)
	config.Images.Registry = registry
	config.Images.WebhookRegistry = registry

	mz, _ := resource.ParseQuantity("10Mi")
	config.Grpc.MaxReceiveSize = &mz
	config.Cloud.SystemaHost = "127.0.0.1"

	if addConfig != nil {
		config.Merge(addConfig)
	}
	configYaml, err := yaml.Marshal(&config)
	if err != nil {
		t.Fatal(err)
	}
	configYamlStr := string(configYaml)

	configDir := t.TempDir()
	c = filelocation.WithAppUserConfigDir(c, configDir)
	c, err = client.SetConfig(c, configDir, configYamlStr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
