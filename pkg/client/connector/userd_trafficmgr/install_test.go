package userd_trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	goRuntime "runtime"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func publishManager(t *testing.T) {
	t.Helper()
	ctx := dlog.NewTestContext(t, false)

	cmd := dexec.CommandContext(ctx, "make", "-C", "../../../..", "push-image")
	if goRuntime.GOOS == "windows" {
		cmd = dexec.CommandContext(ctx, "../../../../winmake.bat", "push-image")
	}

	// Go sets a lot of variables that we don't want to pass on to the ko executable. If we do,
	// then it builds for the platform indicated by those variables.
	cmd.Env = []string{
		"TELEPRESENCE_VERSION=" + version.Version,
		"TELEPRESENCE_REGISTRY=" + dtest.DockerRegistry(ctx),
	}
	includeEnv := []string{"HOME=", "PATH=", "LOGNAME=", "TMPDIR=", "MAKELEVEL="}
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
	ctx := dlog.NewTestContext(t, false)

	dtest.WithMachineLock(ctx, func(ctx context.Context) {
		kubeconfig := dtest.Kubeconfig(ctx)

		testVersion := fmt.Sprintf("v2.0.0-gotest.%d", os.Getpid())
		namespace := fmt.Sprintf("telepresence-%d", os.Getpid())
		managerTestNamespace := fmt.Sprintf("ambassador-%d", os.Getpid())

		version.Version = testVersion

		os.Setenv("DTEST_KUBECONFIG", kubeconfig)
		os.Setenv("DTEST_REGISTRY", dtest.DockerRegistry(ctx)) // Prevent extra calls to dtest.RegistryUp() which may panic

		saveManagerNamespace, ok := os.LookupEnv("TELEPRESENCE_MANAGER_NAMESPACE")
		defer func() {
			if !ok {
				os.Unsetenv("TELEPRESENCE_MANAGER_NAMESPACE")
			} else {
				os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", saveManagerNamespace)
			}
		}()
		os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", managerTestNamespace)
		_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", namespace).Run()
		defer func() {
			_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", managerTestNamespace, "--wait=false").Run()
			_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", namespace, "--wait=false").Run()
		}()

		t.Run("findTrafficManager_notPresent", func(t *testing.T) {
			ctx := dlog.NewTestContext(t, false)
			env, err := client.LoadEnv(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cfgAndFlags, err := userd_k8s.NewConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, env)
			if err != nil {
				t.Fatal(err)
			}
			kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
			if err != nil {
				t.Fatal(err)
			}
			ti, err := newTrafficManagerInstaller(kc)
			if err != nil {
				t.Fatal(err)
			}
			version.Version = "v0.0.0-bogus"
			defer func() { version.Version = testVersion }()

			if _, err := ti.FindDeployment(ctx, managerTestNamespace, install.ManagerAppName); err == nil {
				t.Fatal("expected find to not find deployment")
			}
		})

		t.Run("findTrafficManager_present", func(t *testing.T) {
			findTrafficManagerPresent(t, kubeconfig, managerTestNamespace)
		})

		t.Run("findTrafficManager_differentNamespace_present", func(t *testing.T) {
			oldCfg, err := clientcmd.LoadFromFile(kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				err := clientcmd.WriteToFile(*oldCfg, kubeconfig)
				if err != nil {
					t.Fatal(err)
				}
			}()

			customNamespace := fmt.Sprintf("custom-%d", os.Getpid())
			_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "create", "namespace", customNamespace).Run()
			defer func() {
				_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "delete", "namespace", customNamespace, "--wait=false").Run()
			}()

			// Load the config again so that oldCfg isn't disturbed.
			cfg, err := clientcmd.LoadFromFile(kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			err = api.MinifyConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var cluster *api.Cluster
			for _, c := range cfg.Clusters {
				cluster = c
				break
			}
			if cluster == nil {
				t.Fatal("Unable to get cluster from config")
			}
			cluster.Extensions = map[string]runtime.Object{"telepresence.io": &runtime.Unknown{
				Raw: []byte(fmt.Sprintf(`{"manager":{"namespace": "%s"}}`, customNamespace)),
			}}
			err = clientcmd.WriteToFile(*cfg, kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			findTrafficManagerPresent(t, kubeconfig, customNamespace)
		})

		t.Run("ensureTrafficManager_notPresent", func(t *testing.T) {
			c := dlog.NewTestContext(t, false)
			publishManager(t)
			defer removeManager(t, kubeconfig, managerTestNamespace)
			env, err := client.LoadEnv(c)
			if err != nil {
				t.Fatal(err)
			}
			cfgAndFlags, err := userd_k8s.NewConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, env)
			if err != nil {
				t.Fatal(err)
			}
			kc, err := userd_k8s.NewCluster(c, cfgAndFlags, nil, userd_k8s.Callbacks{})
			if err != nil {
				t.Fatal(err)
			}
			ti, err := newTrafficManagerInstaller(kc)
			if err != nil {
				t.Fatal(err)
			}
			if err := ti.ensureManager(c, &env); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func findTrafficManagerPresent(t *testing.T, kubeconfig, namespace string) {
	c := dlog.NewTestContext(t, false)
	publishManager(t)
	defer removeManager(t, kubeconfig, namespace)

	env, err := client.LoadEnv(c)
	if err != nil {
		t.Fatal(err)
	}

	cfgAndFlags, err := userd_k8s.NewConfig(map[string]string{"kubeconfig": kubeconfig, "namespace": namespace}, env)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := userd_k8s.NewCluster(c, cfgAndFlags, nil, userd_k8s.Callbacks{})
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
		watcherErr <- kc.RunWatchers(watchCtx)
	}()
	waitCtx, waitCancel := context.WithTimeout(c, 10*time.Second)
	defer waitCancel()
	if err := kc.WaitUntilReady(waitCtx); err != nil {
		t.Fatal(err)
	}

	ti, err := newTrafficManagerInstaller(kc)
	if err != nil {
		t.Fatal(err)
	}
	err = ti.ensureManager(c, &env)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := ti.FindDeployment(c, namespace, install.ManagerAppName); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("traffic-manager deployment not found")
}

func TestAddAgentToWorkload(t *testing.T) {
	// Part 1: Build the testcases /////////////////////////////////////////
	type testcase struct {
		InputVersion  string
		InputPortName string
		InputWorkload kates.Object
		InputService  *kates.Service

		OutputWorkload kates.Object
		OutputService  *kates.Service
	}
	testcases := map[string]testcase{}

	dirinfos, err := ioutil.ReadDir("testdata/addAgentToWorkload")
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for _, di := range dirinfos {
		fileinfos, err := ioutil.ReadDir(filepath.Join("testdata/addAgentToWorkload", di.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, fi := range fileinfos {
			if !strings.HasSuffix(fi.Name(), ".input.yaml") {
				continue
			}
			tcName := di.Name() + "/" + strings.TrimSuffix(fi.Name(), ".input.yaml")

			var tc testcase
			var err error

			tc.InputVersion = di.Name()
			if tc.InputVersion == "cur" {
				// Must alway be higher than any actually released version, so pack
				// a bunch of 9's in there.
				tc.InputVersion = fmt.Sprintf("v2.999.999-gotest.%d.%d", os.Getpid(), i)
				i++
			}

			tc.InputWorkload, tc.InputService, tc.InputPortName, err = loadFile(tcName+".input.yaml", tc.InputVersion)
			if err != nil {
				t.Fatal(err)
			}

			tc.OutputWorkload, tc.OutputService, _, err = loadFile(tcName+".output.yaml", tc.InputVersion)
			if err != nil {
				t.Fatal(err)
			}

			testcases[tcName] = tc
		}
	}

	// Part 2: Run the testcases in "install" mode /////////////////////////
	ctx := dlog.NewTestContext(t, true)
	env, err := client.LoadEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// We use the MachineLock here since we have to reset + set the config.yml
	dtest.WithMachineLock(ctx, func(ctx context.Context) {
		// Specify the registry used in the test data
		configDir := t.TempDir()
		err = prepareConfig(ctx, configDir)

		for tcName, tc := range testcases {
			tcName := tcName // "{version-dir}/{yaml-base-name}"
			tc := tc
			if !strings.HasPrefix(tcName, "cur/") {
				// Don't check install for historical snapshots.
				continue
			}

			t.Run(tcName+"/install", func(t *testing.T) {
				ctx := dlog.NewTestContext(t, true)
				ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
				version.Version = tc.InputVersion

				expectedWrk := deepCopyObject(tc.OutputWorkload)
				sanitizeWorkload(expectedWrk)

				expectedSvc := tc.OutputService.DeepCopy()
				sanitizeService(expectedSvc)

				actualWrk, actualSvc, actualErr := addAgentToWorkload(ctx,
					tc.InputPortName,
					managerImageName(ctx), // ignore extensions
					env.ManagerNamespace,
					deepCopyObject(tc.InputWorkload),
					tc.InputService.DeepCopy(),
				)
				if !assert.NoError(t, actualErr) {
					return
				}

				sanitizeWorkload(actualWrk)
				assert.Equal(t, expectedWrk, actualWrk)

				if actualSvc != nil {
					sanitizeService(actualSvc)
					assert.Equal(t, expectedSvc, actualSvc)
				}

				if t.Failed() && os.Getenv("DEV_TELEPRESENCE_GENERATE_GOLD") != "" {
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
					assert.NoError(t, err)
				}
			})
		}
	})

	// Part 3: Run the testcases in "uninstall" mode ///////////////////////

	for tcName, tc := range testcases {
		tc := tc
		t.Run(tcName+"/uninstall", func(t *testing.T) {
			ctx := dlog.NewTestContext(t, true)
			version.Version = tc.InputVersion

			expectedWrk := deepCopyObject(tc.InputWorkload)
			sanitizeWorkload(expectedWrk)

			expectedSvc := tc.InputService.DeepCopy()
			sanitizeService(expectedSvc)

			actualWrk := deepCopyObject(tc.OutputWorkload)
			_, actualErr := undoObjectMods(ctx, actualWrk)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeWorkload(actualWrk)

			actualSvc := tc.OutputService.DeepCopy()
			actualErr = undoServiceMods(ctx, actualSvc)
			if !assert.NoError(t, actualErr) {
				return
			}
			sanitizeService(actualSvc)

			assert.Equal(t, expectedWrk, actualWrk)
			assert.Equal(t, expectedSvc, actualSvc)
		})
	}
}

func sanitizeWorkload(obj kates.Object) {
	obj.SetResourceVersion("")
	obj.SetGeneration(int64(0))
	obj.SetCreationTimestamp(metav1.Time{})
	podTemplate, _ := install.GetPodTemplateFromObject(obj)
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

// loadFile is a helper function that reads test data files and converts them
// to a format that can be used in the tests.
func loadFile(filename, inputVersion string) (workload kates.Object, service *kates.Service, portname string, err error) {
	tmpl, err := template.ParseFiles(filepath.Join("testdata/addAgentToWorkload", filename))
	if err != nil {
		return nil, nil, "", fmt.Errorf("read template: %s: %w", filename, err)
	}

	var buff bytes.Buffer
	err = tmpl.Execute(&buff, map[string]interface{}{
		"Version": strings.TrimPrefix(inputVersion, "v"),
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("execute template: %s: %w", filename, err)
	}

	var dat struct {
		Deployment  *kates.Deployment  `json:"deployment"`
		ReplicaSet  *kates.ReplicaSet  `json:"replicaset"`
		StatefulSet *kates.StatefulSet `json:"statefulset"`

		Service       *kates.Service `json:"service"`
		InterceptPort string         `json:"interceptPort"`
	}
	if err := yaml.Unmarshal(buff.Bytes(), &dat); err != nil {
		return nil, nil, "", fmt.Errorf("parse yaml: %s: %w", filename, err)
	}

	cnt := 0
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
		return nil, nil, "", fmt.Errorf("yaml must contain exactly one of 'deployment', 'replicaset', or 'statefulset'; got %d of them", cnt)
	}

	return workload, dat.Service, dat.InterceptPort, nil
}

// prepareConfig resets the config + sets the registry. Only use within
// withMachineLock
func prepareConfig(ctx context.Context, configDir string) error {
	client.ResetConfig(ctx)
	config, err := os.Create(filepath.Join(configDir, "config.yml"))
	if err != nil {
		return err
	}
	_, err = config.WriteString("images:\n  registry: localhost:5000\n")
	if err != nil {
		return err
	}
	config.Close()
	return nil
}
