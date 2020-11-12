package cli_test

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/datawire/ambassador/pkg/dtest"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
	"github.com/datawire/telepresence2/pkg/version"
)

var testVersion = "v0.1.2-test"
var namespace = fmt.Sprintf("telepresence-%d", os.Getpid())
var proxyOnMatch = regexp.MustCompile(`Proxy:\s+ON`)

var _ = Describe("Telepresence", func() {
	Context("With no daemon running", func() {
		It("Returns version", func() {
			stdout, stderr := execute("--version")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(Equal(fmt.Sprintf("Client %s", client.DisplayVersion())))
		})
		It("Returns valid status", func() {
			out, _ := execute("--status")
			Expect(out).To(ContainSubstring("the telepresence daemon has not been started"))
		})
	})

	Context("With bad KUBECONFIG", func() {
		It("Reports connect error and exits", func() {
			kubeConfig := os.Getenv("KUBECONFIG")
			defer os.Setenv("KUBECONFIG", kubeConfig)
			os.Setenv("KUBECONFIG", "/dev/null")
			stdout, stderr := execute()
			Expect(stderr).To(ContainSubstring("initial cluster check"))
			Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
		})
	})

	Context("When started with a command", func() {
		It("Connects, executes the command, and then exits", func() {
			stdout, stderr := execute("--namespace", namespace, "--", client.GetExe(), "--status")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
			Expect(stdout).To(ContainSubstring("Connected to context"))
			Expect(stdout).To(ContainSubstring("Context:"))
			Expect(stdout).To(MatchRegexp(proxyOnMatch.String()))
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
		})
	})

	Context("When started in the background", func() {
		once := sync.Once{}
		BeforeEach(func() {
			// This is a bit annoying, but ginkgo does not provide a context scoped "BeforeAll"
			once.Do(func() {
				stdout, stderr := execute("--namespace", namespace, "--no-wait")
				Expect(stderr).To(BeEmpty())
				Expect(stdout).To(ContainSubstring("Connected to context"))
			})
		})

		It("Reports version from daemon", func() {
			stdout, stderr := execute("--version")
			Expect(stderr).To(BeEmpty())
			vs := client.DisplayVersion()
			Expect(stdout).To(ContainSubstring(fmt.Sprintf("Client %s", vs)))
			Expect(stdout).To(ContainSubstring(fmt.Sprintf("Daemon %s", vs)))
		})

		It("Reports status as connected", func() {
			stdout, stderr := execute("--status")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Context:"))
		})

		It("Proxies outbound traffic", func() {
			echoReady := make(chan error)
			go func() {
				echoReady <- applyEchoService()
			}()

			// Give outbound interceptor 15 seconds to kick in.
			proxy := false
			for i := 0; i < 30; i++ {
				stdout, stderr := execute("--status")
				Expect(stderr).To(BeEmpty())
				if proxy = proxyOnMatch.MatchString(stdout); proxy {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			Expect(proxy).To(BeTrue(), "Timeout waiting for network overrides to establish")

			err := <-echoReady
			Expect(err).NotTo(HaveOccurred())

			out, err := output("curl", "-s", "echo-easy")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("Request served by echo-easy-"))
		})
	})
})

var _ = BeforeSuite(func() {
	registry := dtest.DockerRegistry()
	kubeconfig := dtest.Kubeconfig()

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)
	os.Setenv("KUBECONFIG", kubeconfig)

	executable, err := buildExecutable(testVersion)
	Expect(err).NotTo(HaveOccurred())

	err = publishManager(testVersion)
	if ee, ok := err.(*exec.ExitError); ok {
		if len(ee.Stderr) > 0 {
			err = errors.New(string(ee.Stderr))
		}
	}
	Expect(err).NotTo(HaveOccurred())

	err = run("kubectl", "create", "namespace", namespace)
	Expect(err).NotTo(HaveOccurred())

	err = run("sudo", "true")
	Expect(err).ToNot(HaveOccurred(), "acquire privileges")

	version.Version = testVersion
	client.SetExe(executable)
})

var _ = AfterSuite(func() {
	_, _ = execute("--quit")
	_ = run("kubectl", "delete", "namespace", namespace)
})

func applyEchoService() error {
	err := run("ko", "apply", "--namespace", namespace, "-f", "k8s/echo-easy.yaml")
	if err != nil {
		return err
	}
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)
		err = run(
			"kubectl", "--namespace", namespace, "run", "curl-from-cluster", "--rm", "-it",
			"--image=pstauffer/curl", "--restart=Never", "--",
			"curl", "--silent", "--output", "/dev/null",
			"http://echo-easy."+namespace,
		)
		if err == nil {
			return nil
		}
	}
	return errors.New("timed out waiting for echo-easy service")
}

// runError checks if the given err is a *exit.ExitError, and if so, extracts
// Stderr and the ExitCode from it.
func runError(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		if len(ee.Stderr) > 0 {
			err = fmt.Errorf("%s, exit code %d", string(ee.Stderr), ee.ExitCode())
		} else {
			err = fmt.Errorf("exit code %d", ee.ExitCode())
		}
	}
	return err
}

func run(args ...string) error {
	return runError(exec.Command(args[0], args[1:]...).Run())
}

func output(args ...string) (string, error) {
	out, err := exec.Command(args[0], args[1:]...).Output()
	return string(out), runError(err)
}

func publishManager(testVersion string) error {
	cmd := exec.Command("ko", "publish", "--local", "./cmd/traffic")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf(`GOFLAGS=-ldflags=-X=github.com/datawire/telepresence2/pkg/version.Version=%s`,
			testVersion))
	out, err := cmd.Output()
	if err != nil {
		return runError(err)
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

// execute the CLI command in-process
func execute(args ...string) (string, string) {
	cmd := getCommand(args...)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
	}
	return trimmed(cmd.OutOrStdout), trimmed(cmd.ErrOrStderr)
}
