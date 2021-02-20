package connector

import (
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
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/version"
)

var kubeconfig string
var namespace string
var registry string
var testVersion string
var managerTestNamespace string

func TestMain(m *testing.M) {
	log.SetOutput(ioutil.Discard) // We want success or failure, not an abundance of output
	kubeconfig = dtest.Kubeconfig()
	testVersion = fmt.Sprintf("0.1.%d", os.Getpid())
	namespace = fmt.Sprintf("telepresence-%d", os.Getpid())
	managerTestNamespace = fmt.Sprintf("ambassador-%d", os.Getpid())

	registry = dtest.DockerRegistry()
	version.Version = testVersion

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)

	var exitCode int
	dtest.WithMachineLock(func() {
		capture(nil, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", namespace)
		defer capture(nil, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", namespace, "--wait=false")
		defer capture(nil, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", managerTestNamespace, "--wait=false")
		exitCode = m.Run()
	})
	os.Exit(exitCode)
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

func publishManager(ctx context.Context, t *testing.T) {
	t.Helper()
	if imageName != "" {
		return
	}

	cmd := dexec.CommandContext(ctx, "make", "-C", "../../..", "push-image")
	cmd.Env = append(os.Environ(),
		"TELEPRESENCE_VERSION="+testVersion,
		"TELEPRESENCE_REGISTRY="+dtest.DockerRegistry())
	if err := cmd.Run(); err != nil {
		t.Fatal(client.RunError(err))
	}
}

func removeManager(ctx context.Context, t *testing.T) {
	// Remove service and deployment
	cmd := dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--namespace", managerNamespace, "delete", "svc,deployment", "traffic-manager")
	_, _ = cmd.Output()

	// Wait until getting them fails
	gone := false
	for cnt := 0; cnt < 10; cnt++ {
		cmd = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--namespace", managerNamespace, "get", "deployment", "traffic-manager")
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
		cmd = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--namespace", managerNamespace, "get", "svc", "traffic-manager")
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
	saveManagerNamespace := managerNamespace
	defer func() {
		managerNamespace = saveManagerNamespace
	}()
	managerNamespace = managerTestNamespace

	ctx := dlog.NewTestContext(t, false)
	cfgAndFlags, err := newK8sConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	kc, err := newKCluster(ctx, cfgAndFlags, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := newTrafficManagerInstaller(kc)
	if err != nil {
		t.Fatal(err)
	}
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = testVersion }()

	if _, err := ti.findDeployment(ctx, managerNamespace, managerAppName); err == nil {
		t.Fatal("expected find to not find deployment")
	}
}

func Test_findTrafficManager_present(t *testing.T) {
	saveManagerNamespace := managerNamespace
	defer func() {
		managerNamespace = saveManagerNamespace
	}()
	managerNamespace = managerTestNamespace

	c := dlog.NewTestContext(t, false)
	publishManager(c, t)
	defer removeManager(c, t)

	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}

	c, cancel := context.WithCancel(c)
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("test-present", func(c context.Context) (err error) {
		// Kill sibling go-routines
		defer cancel()

		cfgAndFlags, err := newK8sConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace})
		if err != nil {
			return err
		}
		kc, err := newKCluster(c, cfgAndFlags, nil, nil)
		if err != nil {
			return err
		}
		accWait := make(chan struct{})
		err = kc.startWatchers(c, accWait)
		if err != nil {
			return err
		}
		<-accWait
		ti, err := newTrafficManagerInstaller(kc)
		if err != nil {
			return err
		}
		_, err = ti.createManagerSvc(c)
		if err != nil {
			return err
		}
		err = ti.createManagerDeployment(c, env)
		if err != nil {
			return err
		}
		for i := 0; i < 50; i++ {
			if _, err := ti.findDeployment(c, managerNamespace, managerAppName); err == nil {
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
	saveManagerNamespace := managerNamespace
	defer func() {
		managerNamespace = saveManagerNamespace
	}()
	managerNamespace = managerTestNamespace
	c := dlog.NewTestContext(t, false)
	publishManager(c, t)
	defer removeManager(c, t)
	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}
	cfgAndFlags, err := newK8sConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	kc, err := newKCluster(c, cfgAndFlags, nil, nil)
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
		inStr := strings.ReplaceAll(string(inBody), "${TELEPRESENCE_VERSION}", testVersion)
		if err = yaml.Unmarshal([]byte(inStr), &tmp); err != nil {
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
		outStr := strings.ReplaceAll(string(outBody), "${TELEPRESENCE_VERSION}", testVersion)
		if err = yaml.Unmarshal([]byte(outStr), &tmp); err != nil {
			t.Fatalf("%s.output.yaml: %v", tcName, err)
		}
		tc.OutputDeployment = tmp.Deployment
		tc.OutputService = tmp.Service

		// If it is a test case for a service with multiple ports,
		// we need to specify the name of the port we want to intercept
		if strings.Contains(tcName, "mp-tc") {
			tc.InputPortName = "https"
		}
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
				managerImageName(env), // ignore extensions
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
