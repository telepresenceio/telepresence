package cli_test

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
	"github.com/datawire/telepresence2/pkg/version"
)

var testVersion = "v0.1.2-test"
var namespace = fmt.Sprintf("telepresence-%d", os.Getpid())

// serviceCount is the number of interceptable services that gets installed
// in the cluster and later intercepted
const serviceCount = 9

func TestTelepresence(t *testing.T) {
	// Check that the "ko" program exists, and adjust PATH as necessary.
	if info, err := os.Stat("../../../tools/bin/ko"); err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0100) == 0 {
		t.Fatal("it looks like the ./tools/bin/ko executable wasn't built; be sure to build it with `make` before running `go test`!")
	}
	toolbindir, err := filepath.Abs("../../../tools/bin")
	if !assert.NoError(t, err) {
		return
	}
	os.Setenv("PATH", toolbindir+":"+os.Getenv("PATH"))

	// Remove very verbose output from DTEST initialization
	log.SetOutput(ioutil.Discard)

	dtest.WithMachineLock(func() {
		_ = os.Chdir("../../..")
		beforeSuite(t)
		defer t.Run("clean up", afterSuite)
		t.Run("Telepresence", testTelepresence)
	})
}

func testTelepresence(t *testing.T) {
	t.Run("With no daemon running", func(t *testing.T) {
		t.Run("Returns version", func(t *testing.T) {
			stdout, stderr := telepresence("version")
			assert.Empty(t, stderr)
			assert.Equal(t, fmt.Sprintf("Client %s", client.DisplayVersion()), stdout)
		})
		t.Run("Returns valid status", func(t *testing.T) {
			out, _ := telepresence("status")
			assert.Contains(t, out, "The telepresence daemon has not been started")
		})
	})

	t.Run("When attempting to connect", func(t *testing.T) {
		t.Run("Using an invalid KUBECONFIG", func(t *testing.T) {
			t.Run("Reports config error and exits", func(t *testing.T) {
				kubeConfig := os.Getenv("KUBECONFIG")
				defer os.Setenv("KUBECONFIG", kubeConfig)
				os.Setenv("KUBECONFIG", "/dev/null")
				stdout, stderr := telepresence("connect")
				assert.Contains(t, stderr, "kubectl config current-context")
				assert.Contains(t, stdout, "Launching Telepresence Daemon")
				assert.Contains(t, stdout, "Daemon quitting")
			})
		})

		t.Run("With non existing context", func(t *testing.T) {
			t.Run("Reports connect error and exits", func(t *testing.T) {
				stdout, stderr := telepresence("connect", "--context", "not-likely-to-exist")
				assert.Contains(t, stderr, `"not-likely-to-exist" does not exist`)
				assert.Contains(t, stdout, "Launching Telepresence Daemon")
				assert.Contains(t, stdout, "Daemon quitting")
			})
		})
	})

	t.Run("When connecting with a command", func(t *testing.T) {
		t.Run("Connects, executes the command, and then exits", func(t *testing.T) {
			stdout, stderr := telepresence("--namespace", namespace, "connect", "--", client.GetExe(), "status")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Launching Telepresence Daemon")
			assert.Contains(t, stdout, "Connected to context")
			assert.Contains(t, stdout, "Context:")
			assert.Regexp(t, `Proxy:\s+ON`, stdout)
			assert.Contains(t, stdout, "Daemon quitting")
		})
	})

	t.Run("When connected", func(t *testing.T) {
		stdout, stderr := telepresence("--namespace", namespace, "connect")
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "Connected to context")

		defer t.Run("clean up", func(t *testing.T) {
			stdout, stderr := telepresence("quit")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "quitting")
			time.Sleep(time.Second) // Allow some time for processes to die and sockets to vanish
		})

		t.Run("Reports version from daemon", func(t *testing.T) {
			stdout, stderr := telepresence("version")
			assert.Empty(t, stderr)
			vs := client.DisplayVersion()
			assert.Contains(t, stdout, fmt.Sprintf("Client %s", vs))
			assert.Contains(t, stdout, fmt.Sprintf("Daemon %s", vs))
		})

		t.Run("Reports status as connected", func(t *testing.T) {
			stdout, stderr := telepresence("status")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Context:")
		})

		t.Run("Proxies outbound traffic", func(t *testing.T) {
			// Give outbound interceptor 15 seconds to kick in.
			assert.Eventually(t,
				// condition
				func() bool {
					stdout, _ := telepresence("status")
					return regexp.MustCompile(`Proxy:\s+ON`).FindString(stdout) != ""
				},
				15*time.Second, // waitFor
				time.Second,    // polling interval
				"Timeout waiting for network overrides to establish", // msg
			)

			for i := 0; i < serviceCount; i++ {
				svc := fmt.Sprintf("hello-%d", i)
				assert.Eventually(t,
					// condition
					func() bool {
						out, _ := output("curl", "-s", svc)
						return strings.Contains(out, fmt.Sprintf("Request served by %s-", svc))
					},
					5*time.Second,        // waitfor
					500*time.Millisecond, // polling interval
				)
			}
		})

		t.Run("Proxies concurrent inbound traffic with intercept", func(t *testing.T) {
			intercepts := make([]string, 0, serviceCount)
			services := make([]*http.Server, 0, serviceCount)

			defer t.Run("cleaning up", func(t *testing.T) {
				for _, svc := range intercepts {
					stdout, stderr := telepresence("leave", svc)
					assert.Empty(t, stderr)
					assert.Empty(t, stdout)
				}
				for _, srv := range services {
					_ = srv.Shutdown(context.Background())
				}
			})

			t.Run("adding intercepts", func(t *testing.T) {
				for i := 0; i < serviceCount; i++ {
					svc := fmt.Sprintf("hello-%d", i)
					port := strconv.Itoa(9000 + i)
					stdout, stderr := telepresence("intercept", svc, "--port", port)
					assert.Empty(t, stderr)
					intercepts = append(intercepts, svc)
					assert.Contains(t, stdout, "Using deployment "+svc)
				}
			})

			t.Run("starting http servers", func(t *testing.T) {
				for i := 0; i < serviceCount; i++ {
					svc := fmt.Sprintf("hello-%d", i)
					port := strconv.Itoa(9000 + i)
					srv := &http.Server{Addr: ":" + port, Handler: http.NewServeMux()}
					go func() {
						srv.Handler.(*http.ServeMux).HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
							fmt.Fprintf(w, "%s from intercept at %s", svc, r.URL.Path)
						})
						services = append(services, srv)
						err := srv.ListenAndServe()
						assert.Equal(t, http.ErrServerClosed, err)
					}()
				}
			})

			t.Run("verifying responses from interceptor", func(t *testing.T) {
				for i := 0; i < serviceCount; i++ {
					svc := fmt.Sprintf("hello-%d", i)
					assert.Eventually(t,
						// condition
						func() bool {
							out, _ := output("curl", "-s", svc)
							return out == fmt.Sprintf("%s from intercept at /", svc)
						},
						5*time.Second,       // waitFor
						50*time.Millisecond, // polling interval
					)
				}
			})

			t.Run("listing active intercepts", func(t *testing.T) {
				stdout, stderr := telepresence("list", "--intercepts")
				assert.Empty(t, stderr)
				for i := 0; i < serviceCount; i++ {
					assert.Contains(t, stdout, fmt.Sprintf("hello-%d: intercepted", i))
				}
			})
		})

		t.Run("Successfully intercepts deployment with probes", func(t *testing.T) {
			stdout, stderr := telepresence("intercept", "with-probes", "--port", "9090")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Using deployment with-probes")
			stdout, stderr = telepresence("list", "--intercepts")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "with-probes: intercepted")
		})
	})

	t.Run("When uninstalling", func(t *testing.T) {
		t.Run("Uninstalls agent on given deployment", func(t *testing.T) {
			agentName := func() (string, error) {
				return kubectlOut("get", "deploy", "with-probes", "-o",
					`jsonpath={.spec.template.spec.containers[?(@.name=="traffic-agent")].name}`)
			}
			stdout, err := agentName()
			assert.NoError(t, err)
			assert.Equal(t, "traffic-agent", stdout)
			_, stderr := telepresence("--namespace", namespace, "uninstall", "--agent", "with-probes")
			assert.Empty(t, stderr)
			defer telepresence("quit")
			assert.Eventually(t,
				// condition
				func() bool {
					stdout, _ := agentName()
					return stdout == ""
				},
				5*time.Second,        // waitFor
				500*time.Millisecond, // polling interval
			)
		})

		t.Run("Uninstalls all agents", func(t *testing.T) {
			agentNames := func() (string, error) {
				return kubectlOut("get", "deploy", "-o",
					`jsonpath={.items[*].spec.template.spec.containers[?(@.name=="traffic-agent")].name}`)
			}
			stdout, err := agentNames()
			assert.NoError(t, err)
			assert.Equal(t, serviceCount, len(strings.Split(stdout, " ")))
			_, stderr := telepresence("--namespace", namespace, "uninstall", "--all-agents")
			assert.Empty(t, stderr)
			defer telepresence("quit")
			assert.Eventually(t,
				func() bool {
					stdout, _ := agentNames()
					return stdout == ""
				},
				5*time.Second,        // waitFor
				500*time.Millisecond, // polling interval
			)
		})

		t.Run("Uninstalls the traffic manager and quits", func(t *testing.T) {
			names := func() (string, error) {
				return kubectlOut("get", "svc,deploy", "traffic-manager", "--ignore-not-found", "-o", "jsonpath={.items[*].metadata.name}")
			}
			stdout, err := names()
			assert.NoError(t, err)
			assert.Equal(t, 2, len(strings.Split(stdout, " "))) // The service and the deployment
			stdout, stderr := telepresence("--namespace", namespace, "uninstall", "--everything")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Daemon quitting")
			assert.Eventually(t,
				func() bool {
					stdout, _ := names()
					t.Log(stdout)
					return stdout == ""
				},
				5*time.Second,        // waitFor
				500*time.Millisecond, // polling interval
			)
		})
	})
}

func beforeSuite(t *testing.T) {
	version.Version = testVersion

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		executable, err := buildExecutable(testVersion)
		assert.NoError(t, err)
		client.SetExe(executable)
	}()

	_ = os.Remove(client.ConnectorSocketName)
	err := run("sudo", "true")
	assert.NoError(t, err, "acquire privileges")

	registry := dtest.DockerRegistry()
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := publishManager(testVersion)
		assert.NoError(t, err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		kubeconfig := dtest.Kubeconfig()
		os.Setenv("DTEST_KUBECONFIG", kubeconfig)
		os.Setenv("KUBECONFIG", kubeconfig)
		err = run("kubectl", "create", "namespace", namespace)
		assert.NoError(t, err)
	}()
	wg.Wait()

	wg.Add(serviceCount)
	for i := 0; i < serviceCount; i++ {
		i := i
		go func() {
			defer wg.Done()
			err = applyEchoService(fmt.Sprintf("hello-%d", i))
			assert.NoError(t, err)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = applyApp("with-probes")
		assert.NoError(t, err)
	}()
	wg.Wait()

	// Ensure that no telepresence is running when the tests start
	_, _ = telepresence("quit")
}

func afterSuite(_ *testing.T) {
	_ = run("kubectl", "delete", "namespace", namespace)
}

func applyApp(name string) error {
	err := kubectl("apply", "-f", fmt.Sprintf("k8s/%s.yaml", name))
	if err != nil {
		return fmt.Errorf("failed to deploy %s: %v", name, err)
	}
	return waitForService(name)
}

func applyEchoService(name string) error {
	err := kubectl("create", "deploy", name, "--image", "jmalloc/echo-server:0.1.0")
	if err != nil {
		return fmt.Errorf("failed to create deployment %s: %v", name, err)
	}
	err = kubectl("expose", "deploy", name, "--port", "80", "--target-port", "8080")
	if err != nil {
		return fmt.Errorf("failed to expose deployment %s: %v", name, err)
	}
	return waitForService(name)
}

func waitForService(name string) error {
	for i := 0; i < 120; i++ {
		time.Sleep(time.Second)
		err := kubectl("run", "curl-from-cluster", "--rm", "-it",
			"--image=pstauffer/curl", "--restart=Never", "--",
			"curl", "--silent", "--output", "/dev/null",
			fmt.Sprintf("http://%s.%s", name, namespace),
		)
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for %s service", name)
}

func kubectl(args ...string) error {
	return run(append([]string{"kubectl", "--namespace", namespace}, args...)...)
}

func kubectlOut(args ...string) (string, error) {
	return output(append([]string{"kubectl", "--namespace", namespace}, args...)...)
}

func run(args ...string) error {
	return client.RunError(exec.Command(args[0], args[1:]...).Run())
}

func output(args ...string) (string, error) {
	out, err := exec.Command(args[0], args[1:]...).Output()
	return string(out), client.RunError(err)
}

func publishManager(testVersion string) error {
	cmd := exec.Command("ko", "publish", "--local", "./cmd/traffic")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf(`GOFLAGS=-ldflags=-X=github.com/datawire/telepresence2/pkg/version.Version=%s`,
			testVersion))
	out, err := cmd.Output()
	if err != nil {
		return client.RunError(err)
	}
	imageName := strings.TrimSpace(string(out))
	tag := fmt.Sprintf("%s/tel2:%s", dtest.DockerRegistry(), testVersion)
	err = run("docker", "tag", imageName, tag)
	if err != nil {
		return err
	}
	return run("docker", "push", tag)
}

func buildExecutable(testVersion string) (string, error) {
	executable := filepath.Join("build-output", "bin", "/telepresence")
	return executable, run("go", "build", "-ldflags",
		fmt.Sprintf("-X=github.com/datawire/telepresence2/pkg/version.Version=%s", testVersion),
		"-o", executable, "./cmd/telepresence")
}

func getCommand(args ...string) *cobra.Command {
	cmd := cli.Command()
	cmd.SetArgs(args)
	flags := cmd.Flags()

	// Circumvent test flag conflict explained here https://golang.org/doc/go1.13#testing
	flag.Visit(func(f *flag.Flag) {
		flags.AddGoFlag(f)
	})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	cmd.SilenceErrors = true
	return cmd
}

func trimmed(f func() io.Writer) string {
	if out, ok := f().(*strings.Builder); ok {
		return strings.TrimSpace(out.String())
	}
	return ""
}

// telepresence executes the CLI command in-process
func telepresence(args ...string) (string, string) {
	cmd := getCommand(args...)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), err)
	}
	return trimmed(cmd.OutOrStdout), trimmed(cmd.ErrOrStderr)
}
