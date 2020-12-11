package cli_test

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
	"github.com/datawire/telepresence2/pkg/version"
)

var testVersion = "v0.1.2-test"
var namespace = fmt.Sprintf("telepresence-%d", os.Getpid())

// serviceCount is the number of interceptable services that gets installed
// in the cluster and later intercepted
const serviceCount = 12

var _ = Describe("Telepresence", func() {
	Context("With no daemon running", func() {
		It("Returns version", func() {
			stdout, stderr := telepresence("version")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(Equal(fmt.Sprintf("Client %s", client.DisplayVersion())))
		})
		It("Returns valid status", func() {
			out, _ := telepresence("status")
			Expect(out).To(ContainSubstring("The telepresence daemon has not been started"))
		})
	})

	Context("When attempting to connect", func() {
		Context("Using an invalid KUBECONFIG", func() {
			It("Reports config error and exits", func() {
				kubeConfig := os.Getenv("KUBECONFIG")
				defer os.Setenv("KUBECONFIG", kubeConfig)
				os.Setenv("KUBECONFIG", "/dev/null")
				stdout, stderr := telepresence("connect")
				Expect(stderr).To(ContainSubstring("kubectl config current-context"))
				Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
				Expect(stdout).To(ContainSubstring("Daemon quitting"))
			})
		})

		Context("With non existing context", func() {
			It("Reports connect error and exits", func() {
				stdout, stderr := telepresence("connect", "--context", "not-likely-to-exist")
				Expect(stderr).To(ContainSubstring(`"not-likely-to-exist" does not exist`))
				Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
				Expect(stdout).To(ContainSubstring("Daemon quitting"))
			})
		})
	})

	Context("When connecting with a command", func() {
		It("Connects, executes the command, and then exits", func() {
			stdout, stderr := telepresence("--namespace", namespace, "connect", "--", client.GetExe(), "status")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
			Expect(stdout).To(ContainSubstring("Connected to context"))
			Expect(stdout).To(ContainSubstring("Context:"))
			Expect(stdout).To(MatchRegexp(`Proxy:\s+ON`))
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
		})
	})

	Context("When connected", func() {
		itCount := int32(0)
		itTotal := int32(0) // To simulate AfterAll. Add one for each added It() test
		BeforeEach(func() {
			// This is a bit annoying, but ginkgo does not provide a context scoped "BeforeAll"
			// Will be fixed in ginkgo 2.0
			if atomic.CompareAndSwapInt32(&itCount, 0, 1) {
				stdout, stderr := telepresence("--namespace", namespace, "connect")
				Expect(stderr).To(BeEmpty())
				Expect(stdout).To(ContainSubstring("Connected to context"))
			} else {
				atomic.AddInt32(&itCount, 1)
			}
		})

		AfterEach(func() {
			// This is a bit annoying, but ginkgo does not provide a context scoped "AfterAll"
			// Will be fixed in ginkgo 2.0
			if atomic.CompareAndSwapInt32(&itCount, itTotal, 0) {
				stdout, stderr := telepresence("quit")
				Expect(stderr).To(BeEmpty())
				Expect(stdout).To(ContainSubstring("quitting"))
			}
		})

		It("Reports version from daemon", func() {
			stdout, stderr := telepresence("version")
			Expect(stderr).To(BeEmpty())
			vs := client.DisplayVersion()
			Expect(stdout).To(ContainSubstring(fmt.Sprintf("Client %s", vs)))
			Expect(stdout).To(ContainSubstring(fmt.Sprintf("Daemon %s", vs)))
		})
		itTotal++

		It("Reports status as connected", func() {
			stdout, stderr := telepresence("status")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Context:"))
		})
		itTotal++

		It("Proxies outbound traffic", func() {
			// Give outbound interceptor 15 seconds to kick in.
			Eventually(func() (string, string) {
				return telepresence("status")
			}, 15*time.Second, time.Second).Should(MatchRegexp(`Proxy:\s+ON`), "Timeout waiting for network overrides to establish")

			for i := 0; i < serviceCount; i++ {
				svc := fmt.Sprintf("hello-%d", i)
				Eventually(func() (string, error) {
					return output("curl", "-s", svc)
				}, 5*time.Second, 500*time.Millisecond).Should(ContainSubstring(fmt.Sprintf("Request served by %s-", svc)))
			}
		})
		itTotal++

		It("Proxies concurrent inbound traffic with intercept", func() {
			intercepts := make([]string, 0, serviceCount)
			services := make([]*http.Server, 0, serviceCount)

			defer func() {
				for _, svc := range intercepts {
					stdout, stderr := telepresence("leave", svc)
					Expect(stderr).To(BeEmpty())
					Expect(stdout).To(BeEmpty())
				}
				for _, srv := range services {
					_ = srv.Shutdown(context.Background())
				}
			}()

			By("adding intercepts", func() {
				for i := 0; i < serviceCount; i++ {
					svc := fmt.Sprintf("hello-%d", i)
					port := strconv.Itoa(9000 + i)
					stdout, stderr := telepresence("intercept", svc, "--port", port)
					Expect(stderr).To(BeEmpty())
					intercepts = append(intercepts, svc)
					Expect(stdout).To(ContainSubstring("Using deployment " + svc))
				}
			})

			By("starting http servers", func() {
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
						Expect(err).To(Equal(http.ErrServerClosed))
					}()
				}
			})

			By("verifying responses from interceptor", func() {
				for i := 0; i < serviceCount; i++ {
					svc := fmt.Sprintf("hello-%d", i)
					Eventually(func() (string, error) {
						return output("curl", "-s", svc)
					}, 5*time.Second, 50*time.Millisecond).Should(Equal(fmt.Sprintf("%s from intercept at /", svc)))
				}
			})

			By("listing active intercepts", func() {
				stdout, stderr := telepresence("list")
				Expect(stderr).To(BeEmpty())
				matches := make([]types.GomegaMatcher, serviceCount)
				for i := 0; i < serviceCount; i++ {
					matches[i] = ContainSubstring(" hello-%d\n", i)
				}
				Expect(stdout).To(And(matches...))
			})
		})
		itTotal++

		It("Successfully intercepts deployment with probes", func() {
			stdout, stderr := telepresence("intercept", "with-probes", "--port", "9090")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Using deployment with-probes"))
			stdout, stderr = telepresence("list")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring(" with-probes\n"))
		})
		itTotal++
	})

	Context("when uninstalling", func() {
		It("Uninstalls agent on given deployment", func() {
			stdout, err := output("kubectl", "--namespace", namespace, "get", "deploy", "with-probes", "-o", "jsonpath={.spec.template.spec.containers[*].name}")
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout).To(ContainSubstring("traffic-agent"))
			_, stderr := telepresence("--namespace", namespace, "uninstall", "--agent", "with-probes")
			Expect(stderr).To(BeEmpty())
			defer telepresence("quit")
			Eventually(func() (string, error) {
				return output("kubectl", "--namespace", namespace, "get", "deploy", "with-probes", "-o", "jsonpath={.spec.template.spec.containers[*].name}")
			}, 5*time.Second, 500*time.Millisecond).ShouldNot(ContainSubstring("traffic-agent"))
		})

		It("Uninstalls all agents", func() {
			stdout, err := output("kubectl", "--namespace", namespace, "get", "deploy", "-o", "jsonpath={.items[*].spec.template.spec.containers[*].name}")
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout).To(ContainSubstring("traffic-agent"))
			_, stderr := telepresence("--namespace", namespace, "uninstall", "--all-agents")
			Expect(stderr).To(BeEmpty())
			defer telepresence("quit")
			Eventually(func() (string, error) {
				return output("kubectl", "--namespace", namespace, "get", "deploy", "-o", "jsonpath={.items[*].spec.template.spec.containers[*].name}")
			}, 5*time.Second, 500*time.Millisecond).ShouldNot(ContainSubstring("traffic-agent"))
		})

		It("Uninstalls the traffic manager and quits", func() {
			stdout, err := output("kubectl", "--namespace", namespace, "get", "deploy", "-o", "jsonpath={.items[*].metadata.name}")
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout).To(ContainSubstring("traffic-manager"))
			stdout, stderr := telepresence("--namespace", namespace, "uninstall", "--everything")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
			Eventually(func() (string, error) {
				return output("kubectl", "--namespace", namespace, "get", "deploy", "-o", "jsonpath={.items[*].metadata.name}")
			}, 5*time.Second, 500*time.Millisecond).ShouldNot(ContainSubstring("traffic-manager"))
		})
	})
})

var _ = BeforeSuite(func() {
	version.Version = testVersion

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		executable, err := buildExecutable(testVersion)
		Expect(err).NotTo(HaveOccurred())
		client.SetExe(executable)
	}()

	_ = os.Remove(client.ConnectorSocketName)
	err := run("sudo", "true")
	Expect(err).ToNot(HaveOccurred(), "acquire privileges")

	registry := dtest.DockerRegistry()
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)

	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		err := publishManager(testVersion)
		Expect(err).NotTo(HaveOccurred())
	}()

	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()

		kubeconfig := dtest.Kubeconfig()
		os.Setenv("DTEST_KUBECONFIG", kubeconfig)
		os.Setenv("KUBECONFIG", kubeconfig)
		err = run("kubectl", "create", "namespace", namespace)
		Expect(err).NotTo(HaveOccurred())
	}()
	wg.Wait()

	wg.Add(serviceCount)
	for i := 0; i < serviceCount; i++ {
		i := i
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			err = applyEchoService(fmt.Sprintf("hello-%d", i))
			Expect(err).NotTo(HaveOccurred())
		}()
	}

	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		err = applyApp("with-probes")
		Expect(err).NotTo(HaveOccurred())
	}()
	wg.Wait()
})

var _ = AfterSuite(func() {
	_ = run("kubectl", "delete", "namespace", namespace)
})

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
