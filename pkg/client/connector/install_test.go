package connector

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"testing"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/dtest"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var kubeconfig string
var namespace string

func TestMain(m *testing.M) {
	kubeconfig = dtest.Kubeconfig()
	namespace = fmt.Sprintf("telepresence-%d", os.Getpid())

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", dtest.DockerRegistry())
	dtest.WithMachineLock(func() {
		runCmd(nil, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", namespace)
		defer runCmd(nil, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", namespace, "--wait=false")
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

func runCmd(t *testing.T, exe string, args ...string) string {
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

var imageRx = regexp.MustCompile(`(?m)\s+Published\s+(.+)$`)

func publishManager(t *testing.T) string {
	t.Helper()
	out := runCmd(t, "ko", "publish", "../../../cmd/traffic")
	if match := imageRx.FindStringSubmatch(out); match != nil {
		return match[1]
	}
	t.Fatal("unable to extract image name ko publish output")
	return ""
}

func Test_findTrafficManager_notPresent(t *testing.T) {
	ManagerImage = "bogus-image-name"
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "find-traffic-manager",
		Work: func(p *supervisor.Process) error {
			ti, err := newTrafficManagerInstaller(kubeconfig, "")
			if err != nil {
				return err
			}
			if _, err = ti.findDeployment(p, namespace); err != nil {
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
	ManagerImage = publishManager(t)
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "install-then-find",
		Work: func(p *supervisor.Process) error {
			ti, err := newTrafficManagerInstaller(kubeconfig, "")
			if err != nil {
				return err
			}
			_, err = ti.createDeployment(p, namespace)
			if err != nil {
				return err
			}
			_, err = ti.findDeployment(p, namespace)
			return err
		},
	})
	for _, err := range sup.Run() {
		t.Error(err)
	}
}

func Test_ensureTrafficManager_notPresent(t *testing.T) {
	ManagerImage = publishManager(t)
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "ensure-traffic-manager",
		Work: func(p *supervisor.Process) error {
			ti, err := newTrafficManagerInstaller(kubeconfig, "")
			if err != nil {
				return err
			}
			sshd, api, err := ti.ensure(p, namespace)
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
