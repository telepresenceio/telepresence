package connector

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/datawire/telepresence2/pkg/client"

	"github.com/datawire/telepresence2/pkg/version"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/dtest"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var kubeconfig string
var namespace string
var registry string
var testVersion = "v0.1.2-test"

func TestMain(m *testing.M) {
	kubeconfig = dtest.Kubeconfig()
	namespace = fmt.Sprintf("telepresence-%d", os.Getpid())
	registry = dtest.DockerRegistry()
	version.Version = testVersion

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)
	dtest.WithMachineLock(func() {
		capture(nil, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", namespace)
		defer capture(nil, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", namespace, "--wait=false")
		os.Exit(m.Run())
	})
}

func showArgs(exe string, args []string) {
	fmt.Print("+ ")
	fmt.Print(exe)
	for _, arg := range args {
		fmt.Print(" ", arg)
	}
	fmt.Println()
}

func capture(t *testing.T, exe string, args ...string) string {
	showArgs(exe, args)
	cmd := exec.Command(exe, args...)
	out, err := cmd.CombinedOutput()
	sout := string(out)
	if err != nil {
		if t != nil {
			t.Fatalf("%s\n%s", sout, err.Error())
		} else {
			log.Fatalf("%s\n%s", sout, err.Error())
		}
	}
	return sout
}

func captureOut(t *testing.T, exe string, args ...string) string {
	showArgs(exe, args)
	cmd := exec.Command(exe, args...)
	out, err := cmd.Output()
	sout := string(out)
	if err != nil {
		if t != nil {
			t.Fatalf("%s\n%s", sout, err.Error())
		} else {
			log.Fatalf("%s\n%s", sout, err.Error())
		}
	}
	return sout
}

var imageName string

func publishManager(t *testing.T) {
	if imageName != "" {
		return
	}
	t.Helper()
	_ = os.Chdir("../../..") // ko must be executed from root to find the .ko.yaml config
	imageName = strings.TrimSpace(captureOut(t, "ko", "publish", "--local", "./cmd/traffic"))
	tag := fmt.Sprintf("%s/tel2:%s", registry, client.Version())
	capture(t, "docker", "tag", imageName, tag)
	capture(t, "docker", "push", tag)
}

func removeManager(t *testing.T) {
	// Remove service and deployment
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "--namespace", namespace, "delete", "svc,deployment", "traffic-manager")
	_, _ = cmd.Output()

	// Wait until getting them fails
	gone := false
	for cnt := 0; cnt < 10; cnt++ {
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfig, "--namespace", namespace, "get", "deployment", "traffic-manager")
		if err := cmd.Run(); err != nil {
			gone = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !gone {
		t.Fatal("timeout waiting for deployment to vanish")
	}
	gone = false
	for cnt := 0; cnt < 10; cnt++ {
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfig, "--namespace", namespace, "get", "svc", "traffic-manager")
		if err := cmd.Run(); err != nil {
			gone = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !gone {
		t.Fatal("timeout waiting for service to vanish")
	}
}

func Test_findTrafficManager_notPresent(t *testing.T) {
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "find-traffic-manager",
		Work: func(p *supervisor.Process) error {
			ti, err := newTrafficManagerInstaller(kubeconfig, "", namespace)
			if err != nil {
				return err
			}
			version.Version = "v0.0.0-bogus"
			defer func() { version.Version = testVersion }()

			if _, err = ti.findDeployment(p, appName); err != nil {
				if kates.IsNotFound(err) {
					return nil
				}
				return err
			}
			return errors.New("expected find to return not-found error")
		},
	})
	for _, err := range sup.Run() {
		t.Error(err)
	}
}

func Test_findTrafficManager_present(t *testing.T) {
	publishManager(t)
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "install-then-find",
		Work: func(p *supervisor.Process) error {
			defer removeManager(t)
			ti, err := newTrafficManagerInstaller(kubeconfig, "", namespace)
			if err != nil {
				return err
			}
			_, err = ti.createManagerDeployment(p)
			if err != nil {
				return err
			}
			_, err = ti.findDeployment(p, appName)
			return err
		},
	})
	for _, err := range sup.Run() {
		t.Error(err)
	}
}

func Test_ensureTrafficManager_notPresent(t *testing.T) {
	publishManager(t)
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "ensure-traffic-manager",
		Work: func(p *supervisor.Process) error {
			defer removeManager(t)
			ti, err := newTrafficManagerInstaller(kubeconfig, "", namespace)
			if err != nil {
				return err
			}
			sshd, api, err := ti.ensureManager(p)
			if err != nil {
				return err
			}
			if sshd != 8022 {
				return errors.New("expected sshd port to be 8082")
			}
			if api != 8081 {
				return errors.New("expected api port to be 8081")
			}
			return nil
		},
	})
	for _, err := range sup.Run() {
		t.Error(err)
	}
}
