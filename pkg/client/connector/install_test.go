package connector

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var kubeconfig string
var namespace string
var registry string
var testVersion string
var managerTestNamespace string

func TestMain(m *testing.M) {
	log.SetOutput(ioutil.Discard) // We want success or failure, not an abundance of output
	kubeconfig = dtest.Kubeconfig()
	testVersion = fmt.Sprintf("v2.0.0-gotest.%d", os.Getpid())
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
	ctx := context.Background()
	if t != nil {
		ctx = dlog.NewTestContext(t, false)
	}
	cmd := dexec.CommandContext(ctx, exe, args...)
	cmd.DisableLogging = true
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

	// Go sets a lot of variables that we don't want to pass on to the ko executable. If we do,
	// then it builds for the platform indicated by those variables.
	cmd.Env = []string{
		"TELEPRESENCE_VERSION=" + testVersion,
		"TELEPRESENCE_REGISTRY=" + dtest.DockerRegistry(),
	}
	includeEnv := []string{"KO_DOCKER_REPO=", "HOME=", "PATH=", "LOGNAME=", "TMPDIR=", "MAKELEVEL="}
	for _, env := range os.Environ() {
		for _, incl := range includeEnv {
			if strings.HasPrefix(env, incl) {
				cmd.Env = append(cmd.Env, env)
				break
			}
		}
	}
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

	cfgAndFlags, err := newK8sConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace})
	if err != nil {
		t.Fatal(err)
	}
	kc, err := newKCluster(c, cfgAndFlags, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	watcherErr := make(chan error)
	watchCtx, watchCancel := context.WithCancel(c)
	defer func() {
		watchCancel()
		if err := <-watcherErr; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		watcherErr <- kc.runWatchers(watchCtx)
	}()
	waitCtx, waitCancel := context.WithTimeout(c, 10*time.Second)
	defer waitCancel()
	if err := kc.waitUntilReady(waitCtx); err != nil {
		t.Fatal(err)
	}

	ti, err := newTrafficManagerInstaller(kc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ti.createManagerSvc(c)
	if err != nil {
		t.Fatal(err)
	}
	err = ti.createManagerDeployment(c, env)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := ti.findDeployment(c, managerNamespace, managerAppName); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("traffic-manager deployment not found")
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

		loadFile := func(filename string) (*kates.Deployment, *kates.Service, error) {
			tmpl, err := template.ParseFiles(filepath.Join("testdata/addAgentToDeployment", filename))
			if err != nil {
				return nil, nil, fmt.Errorf("read template: %s: %w", filename, err)
			}

			var buff bytes.Buffer
			err = tmpl.Execute(&buff, map[string]interface{}{
				"Version": strings.TrimPrefix(testVersion, "v"),
			})
			if err != nil {
				return nil, nil, fmt.Errorf("execute template: %s: %w", filename, err)
			}

			var dat struct {
				Deployment *kates.Deployment `json:"deployment"`
				Service    *kates.Service    `json:"service"`
			}
			if err := yaml.Unmarshal(buff.Bytes(), &dat); err != nil {
				return nil, nil, fmt.Errorf("parse yaml: %s: %w", filename, err)
			}

			return dat.Deployment, dat.Service, nil
		}

		var tc testcase
		var err error

		tc.InputDeployment, tc.InputService, err = loadFile(tcName + ".input.yaml")
		if err != nil {
			t.Fatal(err)
		}

		tc.OutputDeployment, tc.OutputService, err = loadFile(tcName + ".output.yaml")
		if err != nil {
			t.Fatal(err)
		}

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

			expectedDep = tc.InputDeployment.DeepCopy()
			sanitizeDeployment(expectedDep)

			expectedSvc = tc.InputService.DeepCopy()
			sanitizeService(expectedSvc)

			_, actualErr = undoObjectMods(ctx, actualDep)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeDeployment(actualDep)

			actualErr = undoServiceMods(ctx, actualSvc)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeDeployment(actualDep)

			assert.Equal(t, expectedDep, actualDep)
			assert.Equal(t, expectedSvc, actualSvc)
		})
	}
}

// I (Donny) would like to unify this w/ the "TestAddAgentToDeployment
// since this is a lot of copy pasta, I will likely do that when I move
// onto adding StatefulSets
func TestAddAgentToReplicaSet(t *testing.T) {
	type testcase struct {
		InputPortName   string
		InputReplicaSet *kates.ReplicaSet
		InputService    *kates.Service

		OutputReplicaSet *kates.ReplicaSet
		OutputService    *kates.Service
	}
	testcases := map[string]testcase{}

	fileinfos, err := ioutil.ReadDir("testdata/addAgentToReplicaSet")
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range fileinfos {
		if !strings.HasSuffix(fi.Name(), ".input.yaml") {
			continue
		}
		tcName := strings.TrimSuffix(fi.Name(), ".input.yaml")

		loadFile := func(filename string) (*kates.ReplicaSet, *kates.Service, error) {
			tmpl, err := template.ParseFiles(filepath.Join("testdata/addAgentToReplicaSet", filename))
			if err != nil {
				return nil, nil, fmt.Errorf("read template: %s: %w", filename, err)
			}

			var buff bytes.Buffer
			err = tmpl.Execute(&buff, map[string]interface{}{
				"Version": strings.TrimPrefix(testVersion, "v"),
			})
			if err != nil {
				return nil, nil, fmt.Errorf("execute template: %s: %w", filename, err)
			}

			var dat struct {
				ReplicaSet *kates.ReplicaSet `json:"replicaset"`
				Service    *kates.Service    `json:"service"`
			}
			if err := yaml.Unmarshal(buff.Bytes(), &dat); err != nil {
				return nil, nil, fmt.Errorf("parse yaml: %s: %w", filename, err)
			}

			return dat.ReplicaSet, dat.Service, nil
		}

		var tc testcase
		var err error

		tc.InputReplicaSet, tc.InputService, err = loadFile(tcName + ".input.yaml")
		if err != nil {
			t.Fatal(err)
		}

		tc.OutputReplicaSet, tc.OutputService, err = loadFile(tcName + ".output.yaml")
		if err != nil {
			t.Fatal(err)
		}

		// If it is a test case for a service with multiple ports,
		// we need to specify the name of the port we want to intercept
		if strings.Contains(tcName, "mp-") {
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

			expectedDep := tc.OutputReplicaSet.DeepCopy()
			sanitizeDeployment(expectedDep)

			expectedSvc := tc.OutputService.DeepCopy()
			sanitizeService(expectedSvc)

			actualDep, actualSvc, actualErr := addAgentToDeployment(ctx,
				tc.InputPortName,
				managerImageName(env), // ignore extensions
				tc.InputReplicaSet.DeepCopy(),
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

			expectedDep = tc.InputReplicaSet.DeepCopy()
			sanitizeDeployment(expectedDep)

			expectedSvc = tc.InputService.DeepCopy()
			sanitizeService(expectedSvc)

			_, actualErr = undoObjectMods(ctx, actualDep)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeDeployment(actualDep)

			actualErr = undoServiceMods(ctx, actualSvc)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeDeployment(actualDep)

			assert.Equal(t, expectedDep, actualDep)
			assert.Equal(t, expectedSvc, actualSvc)
		})
	}
}

func sanitizeDeployment(obj kates.Object) {
	obj.SetResourceVersion("")
	obj.SetGeneration(int64(0))
	obj.SetCreationTimestamp(metav1.Time{})
	podTemplate, _, _ := GetPodTemplateFromObject(obj)
	for i, c := range podTemplate.Spec.Containers {
		c.TerminationMessagePath = ""
		c.TerminationMessagePolicy = ""
		c.ImagePullPolicy = ""
		podTemplate.Spec.Containers[i] = c
	}
}

func sanitizeService(svc *kates.Service) {
	svc.ObjectMeta.ResourceVersion = ""
	svc.ObjectMeta.Generation = 0
	svc.ObjectMeta.CreationTimestamp = metav1.Time{}
}
