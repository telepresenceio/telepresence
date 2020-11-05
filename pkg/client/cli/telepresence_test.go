package cli_test

import (
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
			stdout, stderr := execute("version")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(Equal(testVersion))
		})
		It("Returns valid status", func() {
			out, _ := execute("--status")
			Expect(out).To(ContainSubstring("The telepresence daemon has not been started"))
		})
	})

	Context("With bad KUBECONFIG", func() {
		It("Reports connect error and exits", func() {
			kubeConfig := os.Getenv("KUBECONFIG")
			defer os.Setenv("KUBECONFIG", kubeConfig)
			os.Setenv("KUBECONFIG", "/dev/null")
			stdout, stderr := execute()
			Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
			Expect(stderr).To(ContainSubstring("initial cluster check"))
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

			out, err := exec.Command("curl", "-s", "echo-easy").Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring("Request served by echo-easy-"))
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
	Expect(err).NotTo(HaveOccurred())

	err = exec.Command("kubectl", "create", "namespace", namespace).Run()
	Expect(err).NotTo(HaveOccurred())

	err = applyEchoService()
	Expect(err).NotTo(HaveOccurred())

	version.Version = testVersion
	client.SetExe(executable)
})

var _ = AfterSuite(func() {
	_, _ = execute("--quit")
	_ = exec.Command("kubectl", "delete", "namespace", namespace).Run()
})

func applyEchoService() error {
	return exec.Command("ko", "apply", "--namespace", namespace, "-f", "k8s/echo-easy.yaml").Run()
}

func publishManager(testVersion string) error {
	out, err := exec.Command("ko", "publish", "--local", "./cmd/traffic").Output()
	if err != nil {
		return err
	}
	imageName := strings.TrimSpace(string(out))
	tag := fmt.Sprintf("%s/tel2:%s", dtest.DockerRegistry(), testVersion)
	err = exec.Command("docker", "tag", imageName, tag).Run()
	if err != nil {
		return err
	}
	return exec.Command("docker", "push", tag).Run()
}

func buildExecutable(testVersion string) (string, error) {
	executable := filepath.Join("build-output", "bin", "/telepresence")
	cmd := exec.Command("go", "build", "-ldflags",
		fmt.Sprintf("-X=github.com/datawire/telepresence2/pkg/version.Version=%s", testVersion),
		"-o", executable, "./cmd/telepresence")
	return executable, cmd.Run()
}

func getCommand(args ...string) *cobra.Command {
	cmd := cli.Command()
	client.AddVersionCommand(cmd)
	cmd.SetArgs(args)
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	return cmd
}

func trimmed(f func() io.Writer) string {
	if out, ok := f().(*strings.Builder); ok {
		return strings.TrimSpace(out.String())
	}
	return ""
}

func execute(args ...string) (string, string) {
	cmd := getCommand(args...)
	err := cmd.Execute()
	if err != nil {
		fmt.Println(cmd.ErrOrStderr(), err.Error())
	}
	return trimmed(cmd.OutOrStdout), trimmed(cmd.ErrOrStderr)
}
