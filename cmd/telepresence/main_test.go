package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/ambassador/pkg/dtest/testprocess"

	"github.com/stretchr/testify/require"
)

var kubeconfig string

func TestMain(m *testing.M) {
	testprocess.Dispatch()
	kubeconfig = dtest.Kubeconfig()
	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	dtest.WithMachineLock(func() {
		os.Exit(m.Run())
	})
}

func showArgs(args []string) {
	fmt.Print("+")
	for _, arg := range args {
		fmt.Print(" ", arg)
	}
	fmt.Println()
}

func run(args ...string) error {
	showArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	return runCmd(cmd)
}

func runCmd(cmd *exec.Cmd) error {
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		fmt.Println("==>", err)
	}
	return err
}

// nolint deadcode
func capture(args ...string) (string, error) {
	showArgs(args)
	cmd := exec.Command(args[0], args[1:]...)
	return captureCmd(cmd)
}

func captureCmd(cmd *exec.Cmd) (string, error) {
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdout = nil
	cmd.Stderr = nil
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if len(out) > 0 {
		fmt.Print(out)
		if out[len(out)-1] != '\n' {
			fmt.Println(" [no newline]")
		}
	}
	if err != nil {
		fmt.Println("==>", err)
	}
	return out, err
}

var executable = filepath.Join("build-output", "bin", "/telepresence")

// doBuildExecutable calls go build
func doBuildExecutable() {
	err := run("go", "build", "-o", executable, ".")
	if err != nil {
		log.Fatalf("build executable: %v", err)
	}
}

var buildExecutable = testprocess.Make(doBuildExecutable)

func TestSmokeOutbound(t *testing.T) {
	var out string
	var err error

	namespace := fmt.Sprintf("telepresence-%d", os.Getpid())
	nsArg := fmt.Sprintf("--namespace=%s", namespace)

	t.Run("setup", func(t *testing.T) {
		require.NoError(t, run("sudo", "true"), "setup: acquire privileges")
		require.NoError(t, run("printenv", "KUBECONFIG"), "setup: ensure cluster is set")
		require.NoError(t, run("sudo", "rm", "-f", "/tmp/telepresence.log"), "setup: remove old log")
		require.NoError(t,
			run("kubectl", "delete", "pod", "teleproxy", "--ignore-not-found", "--wait=true"),
			"setup: check cluster connectivity",
		)
		require.NoError(t, runCmd(buildExecutable), "setup: build executable")
		require.NoError(t, run("kubectl", "create", "namespace", namespace), "setup: create test namespace")
		require.NoError(t,
			run("kubectl", nsArg, "create", "deploy", "hello-world", "--image=ark3/hello-world"),
			"setup: create deployment",
		)
		require.NoError(t,
			run("kubectl", nsArg, "expose", "deploy", "hello-world", "--port=80", "--target-port=8000"),
			"setup: create service",
		)
		require.NoError(t,
			run("kubectl", nsArg, "get", "svc,deploy", "hello-world"),
			"setup: check svc/deploy",
		)
	})

	defer func() {
		require.NoError(t,
			run("kubectl", "delete", "namespace", namespace, "--wait=false"),
			"cleanup: delete test namespace",
		)
	}()

	t.Run("pre-daemon", func(t *testing.T) {
		require.NoError(t, run(executable, "--status"), "status with no daemon")

		fmt.Println("connect")
		require.NoError(t, run(executable, "--no-wait"), "launch daemon and connector")
		require.NoError(t, run(executable, "--version"), "version with daemon")
		require.NoError(t, run(executable, "--status"), "status with daemon")
	})
	windDownOk := false
	defer func() {
		if !windDownOk {
			require.NoError(t, run(executable, "--quit"), "quit daemon")
		}
	}()

	t.Run("await net overrides", func(t *testing.T) {
		for i := 0; i < 120; i++ {
			out, _ := capture(executable, "--status")
			if !strings.Contains(out, "Network overrides NOT established") {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatal("timed out waiting for net overrides")
	})

	t.Run("check connect", func(t *testing.T) {
		out, err = capture(executable, "--status")
		require.NoError(t, err, "status connected")
		if !strings.Contains(out, "Context") {
			t.Fatal("Expected Context in connected status output")
		}
	})
	defer func() {
		require.NoError(t,
			run("kubectl", "delete", "pod", "teleproxy", "--ignore-not-found", "--wait=false"),
			"make next time quicker",
		)
	}()

	t.Run("await bridge", func(t *testing.T) {
		for i := 0; i < 120; i++ {
			out, _ := capture(executable, "--status")
			if strings.Contains(out, "Proxy:         ON") {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		_ = run("kubectl", "get", "pod", "teleproxy")
		t.Fatal("timed out waiting for bridge")
	})

	t.Run("await service", func(t *testing.T) {
		for i := 0; i < 120; i++ {
			err := run(
				"kubectl", nsArg, "run", "curl-from-cluster", "--rm", "-it",
				"--image=pstauffer/curl", "--restart=Never", "--",
				"curl", "--silent", "--output", "/dev/null",
				"http://hello-world."+namespace,
			)
			if err == nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatal("timed out waiting for hello-world service")
	})

	t.Run("check bridge", func(t *testing.T) {
		require.NoError(t, run("curl", "-sv", "hello-world."+namespace), "check bridge")
	})

	t.Run("wind down", func(t *testing.T) {
		out, err = capture(executable, "--status")
		require.NoError(t, err, "status connected")
		if !strings.Contains(out, "Context") {
			t.Fatal("Expected Context in connected status output")
		}
		require.NoError(t, run(executable, "--quit"), "quit")
		windDownOk = true
		out, err = capture(executable, "--status")
		require.NoError(t, err, "status after quit")
		if !strings.Contains(out, "daemon has not been started") {
			t.Fatal("Expected 'daemon has not been started' in status output")
		}
		require.Error(t, run("curl", "-sv", "hello-world."+namespace), "check disconnected")
	})
}
