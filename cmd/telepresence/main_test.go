package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	os.Setenv("KO_DOCKER_REPO", dtest.DockerRegistry())
	os.Setenv("KUBECONFIG", kubeconfig)
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

var imageRx = regexp.MustCompile(`(?m)\s+Published\s+(.+)$`)

func publishManager() (string, error) {
	out, err := capture("ko", "publish", "./cmd/traffic")
	if err != nil {
		return "", err
	}
	if match := imageRx.FindStringSubmatch(out); match != nil {
		return match[1], nil
	}
	return "", errors.New("unable to extract image name ko publish output")
}

var executable = filepath.Join("build-output", "bin", "/telepresence")

// doBuildExecutable calls go build
func doBuildExecutable() {
	imageName, err := publishManager()
	if err != nil {
		log.Fatalf("publish manager: %v", err)
	}
	err = run("go", "build", "-ldflags", fmt.Sprintf("-X=github.com/datawire/telepresence2/pkg/client/connector.ManagerImage=%s", imageName), "-o", executable, "./cmd/telepresence")
	if err != nil {
		log.Fatalf("build executable: %v", err)
	}
}

var buildExecutable = testprocess.Make(doBuildExecutable)

func TestSmokeOutbound(t *testing.T) {
	var out string

	namespace := fmt.Sprintf("telepresence-%d", os.Getpid())
	nsArg := fmt.Sprintf("--namespace=%s", namespace)

	require.NoError(t, run("sudo", "true"), "setup: acquire privileges")
	require.NoError(t, run("printenv", "KUBECONFIG"), "setup: ensure cluster is set")
	_ = os.Chdir("../..") // relative to cmd/telepresence package
	require.NoError(t, runCmd(buildExecutable), "setup: build executable")
	_ = run(executable, "--quit")
	require.NoError(t, run("rm", "-f", "/tmp/telepresence-connector.socket"), "setup: remove old connector socket")
	require.NoError(t, run("sudo", "rm", "-f", "/tmp/telepresence.log"), "setup: remove old log")
	require.NoError(t,
		run("kubectl", "delete", "pod", "--selector", "app=traffic-manager", "--ignore-not-found", "--wait=true"),
		"setup: check cluster connectivity",
	)
	require.NoError(t, run("kubectl", "create", "namespace", namespace), "setup: create test namespace")

	defer func() {
		require.NoError(t,
			run("kubectl", "delete", "namespace", namespace, "--wait=false"),
			"cleanup: delete test namespace",
		)
	}()

	require.NoError(t, run("ko", "apply", "-n", namespace, "-f", "k8s/echo-easy.yaml"), "setup: create deployment")
	require.NoError(t, run("kubectl", nsArg, "get", "svc,deploy", "echo-easy"), "setup: check svc/deploy")

	require.NoError(t, run(executable, "--status"), "status with no daemon")
	fmt.Println("connect")
	require.NoError(t, run(executable, nsArg, "--no-wait"), "launch daemon and connector")

	defer func() {
		require.NoError(t, run(executable, "--quit"), "quit daemon")
		require.Error(t, run("curl", "-sv", "echo-easy."+namespace), "check disconnected")
	}()

	require.NoError(t, run(executable, "--version"), "version with daemon")
	require.NoError(t, run("kubectl", nsArg, "get", "svc,deploy", "traffic-manager"), "setup: check svc/deploy of traffic-manager")
	tmRunning := false
	for i := 0; i < 120; i++ {
		out, _ = capture("kubectl", nsArg, "get", "pod", "--selector", "app=traffic-manager")
		if strings.Contains(out, "Running") {
			tmRunning = true
			break
		}
		if strings.Contains(out, "Error") {
			_, _ = capture("kubectl", nsArg, "describe", "pod", "--selector", "app=traffic-manager")
			_, _ = capture("kubectl", nsArg, "logs", "--selector", "app=traffic-manager")
			t.Fatal("pod error")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !tmRunning {
		t.Fatal("timed out waiting for traffic manager pod")
	}

	connected := false
	for i := 0; i < 120; i++ {
		out, _ := capture(executable, "--status")
		if strings.Contains(out, "The telepresence daemon has not been started") {
			t.Fatal("daemon was unable to start")
		}
		if strings.Contains(out, "Context") {
			connected = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !connected {
		t.Fatal("timed out waiting for net overrides")
	}

	defer func() {
		require.NoError(t,
			run("kubectl", nsArg, "delete", "pod", "--selector", "app=traffic-manager", "--ignore-not-found", "--wait=false"),
			"make next time quicker",
		)
	}()

	bridgeOk := false
	for i := 0; i < 120; i++ {
		out, _ := capture(executable, "--status")
		if strings.Contains(out, "Proxy:         ON") {
			bridgeOk = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !bridgeOk {
		t.Fatal("timed out waiting for bridge")
	}

	serviceOk := false
	for i := 0; i < 120; i++ {
		err := run(
			"kubectl", nsArg, "run", "curl-from-cluster", "--rm", "-it",
			"--image=pstauffer/curl", "--restart=Never", "--",
			"curl", "--silent", "--output", "/dev/null",
			"http://echo-easy."+namespace,
		)
		if err == nil {
			serviceOk = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !serviceOk {
		t.Fatal("timed out waiting for echo-easy service")
	}

	require.NoError(t, run("curl", "-sv", "echo-easy."+namespace), "check bridge")
}
