package connector

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
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

func publishManager(t *testing.T) {
	t.Helper()
	ctx := dlog.NewTestContext(t, false)

	cmd := dexec.CommandContext(ctx, "make", "-C", "../../..", "push-image")

	// Go sets a lot of variables that we don't want to pass on to the ko executable. If we do,
	// then it builds for the platform indicated by those variables.
	cmd.Env = []string{
		"TELEPRESENCE_VERSION=" + version.Version,
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

func removeManager(t *testing.T, kubeconfig, managerNamespace string) {
	ctx := dlog.NewTestContext(t, false)

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

func TestE2E(t *testing.T) {
	kubeconfig := dtest.Kubeconfig()
	testVersion := fmt.Sprintf("v2.0.0-gotest.%d", os.Getpid())
	namespace := fmt.Sprintf("telepresence-%d", os.Getpid())
	managerTestNamespace := fmt.Sprintf("ambassador-%d", os.Getpid())

	registry := dtest.DockerRegistry()
	version.Version = testVersion

	os.Setenv("DTEST_KUBECONFIG", kubeconfig)
	os.Setenv("KO_DOCKER_REPO", registry)
	os.Setenv("TELEPRESENCE_REGISTRY", registry)

	dtest.WithMachineLock(func() {
		ctx := dlog.NewTestContext(t, false)
		_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", namespace).Run()
		defer func() {
			_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", managerTestNamespace, "--wait=false").Run()
			_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", namespace, "--wait=false").Run()
		}()

		t.Run("findTrafficManager_notPresent", func(t *testing.T) {
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
		})

		t.Run("findTrafficManager_present", func(t *testing.T) {
			saveManagerNamespace := managerNamespace
			defer func() {
				managerNamespace = saveManagerNamespace
			}()
			managerNamespace = managerTestNamespace

			c := dlog.NewTestContext(t, false)
			publishManager(t)
			defer removeManager(t, kubeconfig, managerNamespace)

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
		})

		t.Run("ensureTrafficManager_notPresent", func(t *testing.T) {
			saveManagerNamespace := managerNamespace
			defer func() {
				managerNamespace = saveManagerNamespace
			}()
			managerNamespace = managerTestNamespace
			c := dlog.NewTestContext(t, false)
			publishManager(t)
			defer removeManager(t, kubeconfig, managerNamespace)
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
		})
	})
}

func TestAddAgentToWorkload(t *testing.T) {
	version.Version = fmt.Sprintf("v2.0.0-gotest.%d", os.Getpid())
	os.Setenv("TELEPRESENCE_REGISTRY", "localhost:5000")

	type testcase struct {
		InputPortName string
		InputWorkload kates.Object
		InputService  *kates.Service

		OutputWorkload kates.Object
		OutputService  *kates.Service
	}
	testcases := map[string]testcase{}

	fileinfos, err := ioutil.ReadDir("testdata/addAgentToWorkload")
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range fileinfos {
		if !strings.HasSuffix(fi.Name(), ".input.yaml") {
			continue
		}
		tcName := strings.TrimSuffix(fi.Name(), ".input.yaml")

		loadFile := func(filename string) (kates.Object, *kates.Service, error) {
			tmpl, err := template.ParseFiles(filepath.Join("testdata/addAgentToWorkload", filename))
			if err != nil {
				return nil, nil, fmt.Errorf("read template: %s: %w", filename, err)
			}

			var buff bytes.Buffer
			err = tmpl.Execute(&buff, map[string]interface{}{
				"Version": strings.TrimPrefix(version.Version, "v"),
			})
			if err != nil {
				return nil, nil, fmt.Errorf("execute template: %s: %w", filename, err)
			}

			var dat struct {
				Deployment  *kates.Deployment  `json:"deployment"`
				ReplicaSet  *kates.ReplicaSet  `json:"replicaset"`
				StatefulSet *kates.StatefulSet `json:"statefulset"`

				Service *kates.Service `json:"service"`
			}
			if err := yaml.Unmarshal(buff.Bytes(), &dat); err != nil {
				return nil, nil, fmt.Errorf("parse yaml: %s: %w", filename, err)
			}

			cnt := 0
			var workload kates.Object
			if dat.Deployment != nil {
				cnt++
				workload = dat.Deployment
			}
			if dat.ReplicaSet != nil {
				cnt++
				workload = dat.ReplicaSet
			}
			if dat.StatefulSet != nil {
				cnt++
				workload = dat.StatefulSet
			}
			if cnt != 1 {
				return nil, nil, fmt.Errorf("yaml must contain exactly one of 'deployment', 'replicaset', or 'statefulset'; got %d of them", cnt)
			}

			return workload, dat.Service, nil
		}

		var tc testcase
		var err error

		tc.InputWorkload, tc.InputService, err = loadFile(tcName + ".input.yaml")
		if err != nil {
			t.Fatal(err)
		}

		tc.OutputWorkload, tc.OutputService, err = loadFile(tcName + ".output.yaml")
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
		tcName := tcName
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, true)

			expectedWrk := deepCopyObject(tc.OutputWorkload)
			sanitizeWorkload(expectedWrk)

			expectedSvc := tc.OutputService.DeepCopy()
			sanitizeService(expectedSvc)

			actualWrk, actualSvc, actualErr := addAgentToWorkload(ctx,
				tc.InputPortName,
				managerImageName(env), // ignore extensions
				deepCopyObject(tc.InputWorkload),
				tc.InputService.DeepCopy(),
			)
			if !assert.NoError(t, actualErr) {
				return
			}

			sanitizeWorkload(actualWrk)
			if actualSvc == nil {
				actualSvc = tc.InputService.DeepCopy()
			}
			sanitizeService(actualSvc)

			goldOK := true
			goldOK = goldOK && assert.Equal(t, expectedWrk, actualWrk)
			goldOK = goldOK && assert.Equal(t, expectedSvc, actualSvc)
			if os.Getenv("DEV_TELEPRESENCE_GENERATE_GOLD") != "" && !goldOK {
				workloadKind := actualWrk.GetObjectKind().GroupVersionKind().Kind

				goldBytes, err := yaml.Marshal(map[string]interface{}{
					strings.ToLower(workloadKind): actualWrk,
					"service":                     actualSvc,
				})
				if !assert.NoError(t, err) {
					return
				}
				goldBytes = bytes.ReplaceAll(goldBytes,
					[]byte(strings.TrimPrefix(version.Version, "v")),
					[]byte("{{.Version}}"))

				err = ioutil.WriteFile(
					filepath.Join("testdata/addAgentToWorkload", tcName+".output.yaml"),
					goldBytes,
					0644)
				if !assert.NoError(t, err) {
					return
				}
			}

			expectedWrk = deepCopyObject(tc.InputWorkload)
			sanitizeWorkload(expectedWrk)

			expectedSvc = tc.InputService.DeepCopy()
			sanitizeService(expectedSvc)

			_, actualErr = undoObjectMods(ctx, actualWrk)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeWorkload(actualWrk)

			actualErr = undoServiceMods(ctx, actualSvc)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeWorkload(actualWrk)

			assert.Equal(t, expectedWrk, actualWrk)
			assert.Equal(t, expectedSvc, actualSvc)
		})
	}
}

func sanitizeWorkload(obj kates.Object) {
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

func deepCopyObject(obj kates.Object) kates.Object {
	objValue := reflect.ValueOf(obj)
	retValues := objValue.MethodByName("DeepCopy").Call([]reflect.Value{})
	return retValues[0].Interface().(kates.Object)
}
