package connector

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/version"
)

var kubeconfig string
var namespace string
var registry string
var testVersion = "v0.1.2-test"

func TestMain(m *testing.M) {
	log.SetOutput(ioutil.Discard) // We want success or failure, not an abundance of output
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
			t.Fatalf("%s\n%v", sout, err)
		} else {
			log.Fatalf("%s\n%v", sout, err)
		}
	}
	return sout
}

var imageName string

func publishManager(t *testing.T) {
	t.Helper()
	if imageName != "" {
		return
	}

	// Check that the "ko" program exists, and adjust PATH as necessary.
	if info, err := os.Stat("../../../tools/bin/ko"); err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0100) == 0 {
		t.Fatal("it looks like the ./tools/bin/ko executable wasn't built; be sure to build it with `make` before running `go test`!")
	}
	toolbindir, err := filepath.Abs("../../../tools/bin")
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	os.Setenv("PATH", toolbindir+":"+os.Getenv("PATH"))

	// OK, now run "ko" and friends.
	cmd := exec.Command("ko", "publish", "--local", "./cmd/traffic")
	cmd.Dir = "../../.." // ko must be executed from root to find the .ko.yaml config
	errCapture := bytes.Buffer{}
	cmd.Stderr = &errCapture
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s\n%v", errCapture.String(), err)
	}
	imageName = strings.TrimSpace(string(stdout))
	tag := fmt.Sprintf("%s/tel2:%s", registry, strings.TrimPrefix(client.Version(), "v"))
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
	ctx := dlog.NewTestContext(t, false)
	kc, err := newKCluster(ctx, map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := newTrafficManagerInstaller(kc)
	if err != nil {
		t.Fatal(err)
	}
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = testVersion }()

	if dep := ti.findDeployment(managerAppName); dep != nil {
		t.Fatal("expected find to not find deployment")
	}
}

func Test_findTrafficManager_present(t *testing.T) {
	c := dlog.NewTestContext(t, false)
	publishManager(t)
	defer removeManager(t)

	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}

	c, cancel := context.WithCancel(c)
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("test-present", func(c context.Context) (err error) {
		// Kill sibling go-routines
		defer cancel()

		kc, err := newKCluster(c, map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, nil)
		if err != nil {
			return err
		}
		accWait := make(chan struct{})
		err = kc.startWatches(c, namespace, accWait)
		if err != nil {
			return err
		}
		<-accWait
		ti, err := newTrafficManagerInstaller(kc)
		if err != nil {
			return err
		}
		err = ti.createManagerDeployment(c, env)
		if err != nil {
			return err
		}
		for i := 0; i < 50; i++ {
			if dep := ti.findDeployment(managerAppName); dep != nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("traffic-manager deployment not found")
	})
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}

func Test_ensureTrafficManager_notPresent(t *testing.T) {
	c := dlog.NewTestContext(t, false)
	publishManager(t)
	defer removeManager(t)
	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := newKCluster(c, map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := newTrafficManagerInstaller(kc)
	if err != nil {
		t.Fatal(err)
	}
	if err := ti.ensureManager(c, env); err != nil {
		t.Fatal(err)
	}
}

func TestAddAgentToDeployment(t *testing.T) {
	type testcase struct {
		InputPortName   string
		InputDeployment *kates.Deployment
		InputService    *kates.Service

		OutputDeployment *kates.Deployment
		OutputService    *kates.Service
	}
	testcases := map[string]testcase{}

	fileinfos, err := ioutil.ReadDir("testdata/addAgentToDeployment")
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range fileinfos {
		if !strings.HasSuffix(fi.Name(), ".input.yaml") {
			continue
		}
		tcName := strings.TrimSuffix(fi.Name(), ".input.yaml")

		var tmp struct {
			Deployment *kates.Deployment `json:"deployment"`
			Service    *kates.Service    `json:"service"`
		}

		var tc testcase

		inBody, err := ioutil.ReadFile(filepath.Join("testdata/addAgentToDeployment", fi.Name()))
		if err != nil {
			t.Fatalf("%s.input.yaml: %v", tcName, err)
		}
		if err := yaml.Unmarshal(inBody, &tmp); err != nil {
			t.Fatalf("%s.input.yaml: %v", tcName, err)
		}
		tc.InputDeployment = tmp.Deployment
		tc.InputService = tmp.Service
		tmp.Deployment = nil
		tmp.Service = nil

		outBody, err := ioutil.ReadFile(filepath.Join("testdata/addAgentToDeployment", tcName+".output.yaml"))
		if err != nil {
			t.Fatalf("%s.output.yaml: %v", tcName, err)
		}
		if err := yaml.Unmarshal(outBody, &tmp); err != nil {
			t.Fatalf("%s.output.yaml: %v", tcName, err)
		}
		tc.OutputDeployment = tmp.Deployment
		tc.OutputService = tmp.Service

		testcases[tcName] = tc
	}

	env, err := client.LoadEnv(dlog.NewTestContext(t, true))
	if err != nil {
		t.Fatal(err)
	}
	for tcName, tc := range testcases {
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, true)

			expectedDep := tc.OutputDeployment.DeepCopy()
			sanitizeDeployment(expectedDep)

			expectedSvc := tc.OutputService.DeepCopy()
			sanitizeService(expectedSvc)

			actualDep, actualSvc, actualErr := addAgentToDeployment(ctx,
				tc.InputPortName,
				agentImageName(ctx, env),
				tc.InputDeployment.DeepCopy(),
				[]*kates.Service{tc.InputService.DeepCopy()},
			)
			if !assert.NoError(t, actualErr) {
				return
			}

			sanitizeDeployment(actualDep)
			if actualSvc == nil {
				actualSvc = tc.InputService.DeepCopy()
			}
			sanitizeService(actualSvc)

			assert.Equal(t, expectedDep, actualDep)
			assert.Equal(t, expectedSvc, actualSvc)
		})
	}
}

func sanitizeDeployment(dep *kates.Deployment) {
	dep.ObjectMeta.ResourceVersion = ""
	dep.ObjectMeta.Generation = 0
	dep.ObjectMeta.CreationTimestamp = metav1.Time{}
	for i, c := range dep.Spec.Template.Spec.Containers {
		c.TerminationMessagePath = ""
		c.TerminationMessagePolicy = ""
		c.ImagePullPolicy = ""
		dep.Spec.Template.Spec.Containers[i] = c
	}
}

func sanitizeService(svc *kates.Service) {
	svc.ObjectMeta.ResourceVersion = ""
	svc.ObjectMeta.Generation = 0
	svc.ObjectMeta.CreationTimestamp = metav1.Time{}
}
