package cli_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/datawire/ambassador/pkg/dtest"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cli"
	"github.com/datawire/telepresence2/pkg/version"
)

var testVersion = "v0.1.2-test"
var kubeconfig string
var registry string

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
			stdout, stderr := execute("--", client.GetExe(), "--status")
			Expect(stderr).To(BeEmpty())
			Expect(stdout).To(ContainSubstring("Launching Telepresence Daemon"))
			Expect(stdout).To(ContainSubstring("Connected to context"))
			Expect(stdout).To(ContainSubstring("Context:"))
			Expect(stdout).To(MatchRegexp(`Proxy:\s+ON`))
			Expect(stdout).To(ContainSubstring("Daemon quitting"))
		})
	})

	Context("When started in the background", func() {
		once := sync.Once{}
		BeforeEach(func() {
			once.Do(func() {
				stdout, stderr := execute("--no-wait")
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
	})
})

var _ = BeforeSuite(func() {
	kubeconfig = dtest.Kubeconfig()
	registry = dtest.DockerRegistry()

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)
	os.Setenv("KUBECONFIG", kubeconfig)

	executable, err := buildExecutable(testVersion)
	Expect(err).NotTo(HaveOccurred())
	err = publishManager(testVersion)
	Expect(err).NotTo(HaveOccurred())

	version.Version = testVersion
	client.SetExe(executable)
})

var _ = AfterSuite(func() {
	_, _ = execute("--quit")
})

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
