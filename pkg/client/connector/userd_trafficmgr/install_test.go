package userd_trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goRuntime "runtime"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type installSuite struct {
	suite.Suite
	isCI                 bool
	kubeConfig           string
	testVersion          string
	namespace            string
	managerNamespace     string
	saveManagerNamespace string
}

func (is *installSuite) publishManager() {
	is.T().Helper()
	ctx := dlog.NewTestContext(is.T(), false)

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
	is.Require().NoError(cmd.Run())
}

func (is *installSuite) removeManager(namespace string) {
	require := is.Require()
	ctx := dlog.NewTestContext(is.T(), false)

	// Run a helm uninstall
	cmd := dexec.CommandContext(ctx, "../../../../tools/bin/helm", "--kubeconfig", is.kubeConfig, "--namespace", namespace, "uninstall", "traffic-manager")
	_, _ = cmd.Output()

	// Wait until getting the resources fails
	require.Eventually(func() bool {
		cmd = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "--namespace", namespace, "get", "deployment", "traffic-manager")
		return cmd.Run() != nil
	}, 5*time.Second, time.Second, "timeout waiting for deployment to vanish")

	require.Eventually(func() bool {
		cmd = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "--namespace", namespace, "get", "svc", "traffic-manager")
		return cmd.Run() != nil
	}, 5*time.Second, time.Second, "timeout waiting for service to vanish")
}

func TestE2E(t *testing.T) {
	t.Skip()
	dtest.WithMachineLock(dlog.NewTestContext(t, false), func(ctx context.Context) {
		suite.Run(t, new(installSuite))
	})
}

func (is *installSuite) SetupSuite() {
	ctx := dlog.NewTestContext(is.T(), false)
	is.kubeConfig = dtest.Kubeconfig(ctx)

	suffix, isCI := os.LookupEnv("CIRCLE_SHA1")
	is.isCI = isCI
	if isCI {
		// Use 7 characters of SHA to avoid busting k8s 60 character name limit
		if len(suffix) > 7 {
			suffix = suffix[:7]
		}
	} else {
		suffix = strconv.Itoa(os.Getpid())
	}
	suffix += "-install-test" // Avoid namespace collision with other integration tests

	is.testVersion = fmt.Sprintf("v2.4.5-gotest.%s", suffix)
	is.namespace = fmt.Sprintf("telepresence-%s", suffix)
	is.managerNamespace = fmt.Sprintf("ambassador-%s", suffix)

	version.Version = is.testVersion

	os.Setenv("DTEST_KUBECONFIG", is.kubeConfig)
	os.Setenv("DTEST_REGISTRY", dtest.DockerRegistry(ctx)) // Prevent extra calls to dtest.RegistryUp() which may panic

	is.saveManagerNamespace = os.Getenv("TELEPRESENCE_MANAGER_NAMESPACE")
	os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", is.managerNamespace)
	_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "create", "namespace", is.namespace).Run()

	if !is.isCI {
		is.publishManager()
	}
}

func (is *installSuite) TearDownSuite() {
	ctx := dlog.NewTestContext(is.T(), false)
	if is.saveManagerNamespace == "" {
		os.Unsetenv("TELEPRESENCE_MANAGER_NAMESPACE")
	} else {
		os.Setenv("TELEPRESENCE_MANAGER_NAMESPACE", is.saveManagerNamespace)
	}
	_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "delete", "namespace", is.managerNamespace, "--wait=false").Run()
	_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "delete", "namespace", is.namespace, "--wait=false").Run()
}

func (is *installSuite) Test_findTrafficManager_notPresent() {
	require := is.Require()
	ctx := is.initContext()
	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.namespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)
	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)
	version.Version = "v0.0.0-bogus"

	defer func() { version.Version = is.testVersion }()
	_, err = ti.FindDeployment(ctx, is.managerNamespace, install.ManagerAppName)
	is.Error(err, "expected find to not find deployment")
}

func (is *installSuite) Test_ensureTrafficManager_updateFromLegacy() {
	require := is.Require()
	ctx := is.initContext()

	f, err := os.ReadFile("testdata/legacyManifests/manifests.yml")
	require.NoError(err)
	manifest := string(f)
	manifest = strings.ReplaceAll(manifest, "NAMESPACE", is.managerNamespace)
	cmd := dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "-n", is.managerNamespace, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)

	err = cmd.Run()
	require.NoError(err)

	cmd = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "-n", is.managerNamespace, "rollout", "status", "-w", "deploy/traffic-manager")
	require.NoError(cmd.Run())
	is.findTrafficManagerPresent(is.managerNamespace)
}

func (is *installSuite) Test_ensureTrafficManager_toleratesFailedInstall() {
	require := is.Require()
	ctx := is.initContext()
	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	version.Version = "v0.0.0-bogus"

	restoreVersion := func() { version.Version = is.testVersion }

	// We'll call this further down, but defer it to prevent polluting other tests if we don't leave this function gracefully
	defer restoreVersion()

	defer is.removeManager(is.managerNamespace)

	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)

	require.Error(ti.EnsureManager(ctx))
	restoreVersion()

	require.Eventually(func() bool {
		err = ti.EnsureManager(ctx)
		return err == nil
	}, 20*time.Second, 5*time.Second, "Unable to install proper manager after failed install: %v", err)
}

func (is *installSuite) Test_ensureTrafficManager_toleratesLeftoverState() {
	require := is.Require()
	ctx := is.initContext()

	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)

	require.NoError(ti.EnsureManager(ctx))
	defer is.removeManager(is.managerNamespace)
	err = dexec.CommandContext(ctx, "../../../../tools/bin/helm", "--kubeconfig", is.kubeConfig, "--namespace", is.managerNamespace, "uninstall", "--keep-history", "traffic-manager").Run()
	require.NoError(err)

	require.NoError(ti.EnsureManager(ctx))
	require.Eventually(func() bool {
		deploy, err := ti.FindDeployment(ctx, is.managerNamespace, install.ManagerAppName)
		if err != nil {
			return false
		}
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 10*time.Second, time.Second, "timeout waiting for deployment to update")
}

func (is *installSuite) Test_ensureTrafficManager_canUninstall() {
	require := is.Require()
	ctx := is.initContext()

	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)

	require.NoError(ti.EnsureManager(ctx))
	require.NoError(ti.RemoveManagerAndAgents(ctx, false, []*manager.AgentInfo{}))
	// We want to make sure that we can re-install the agent after it's been uninstalled,
	// so try to EnsureManager again.
	require.NoError(ti.EnsureManager(ctx))
	// Uninstall the agent one last time -- this should behave the same way as the previous uninstall
	require.NoError(ti.RemoveManagerAndAgents(ctx, false, []*manager.AgentInfo{}))
}

func (is *installSuite) Test_ensureTrafficManager_upgrades() {
	require := is.Require()
	ctx := is.initContext()

	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)

	require.NoError(ti.EnsureManager(ctx))
	defer is.removeManager(is.managerNamespace)

	version.Version = "v3.0.0-bogus"
	restoreVersion := func() { version.Version = is.testVersion }
	defer restoreVersion()

	require.Error(ti.EnsureManager(ctx))

	require.Eventually(func() bool {
		deploy, err := ti.FindDeployment(ctx, is.managerNamespace, install.ManagerAppName)
		if err != nil {
			return false
		}
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 10*time.Second, time.Second, "timeout waiting for deployment to update")

	restoreVersion()
	require.NoError(ti.EnsureManager(ctx))
}

func (is *installSuite) Test_ensureTrafficManager_doesNotChangeExistingHelm() {
	require := is.Require()
	ctx := is.initContext()

	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	// The helm chart is declared as 1.9.9 to make sure it's "older" than ours, but we set the tag to 2.4.0 so that it actually starts up.
	// 2.4.0 was the latest release at the time that testdata/telepresence-1.9.9.tgz was packaged
	err = dexec.CommandContext(ctx,
		"../../../../tools/bin/helm",
		"--kubeconfig", is.kubeConfig, "-n", is.managerNamespace,
		"install", "traffic-manager", "testdata/telepresence-1.9.9.tgz",
		"--create-namespace",
		"--atomic",
		"--set", "clusterID="+kc.GetClusterId(ctx),
		"--set", "image.tag=2.4.0",
	).Run()
	require.NoError(err)

	defer is.removeManager(is.managerNamespace)

	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)

	require.NoError(ti.EnsureManager(ctx))

	kc.Client().InvalidateCache()
	dep, err := ti.FindDeployment(ctx, is.managerNamespace, install.ManagerAppName)
	require.NoError(err)
	require.NotNil(dep)
	require.Contains(dep.Spec.Template.Spec.Containers[0].Image, "2.4.0")
	require.Equal(dep.Labels["helm.sh/chart"], "telepresence-1.9.9")
}

func (is *installSuite) Test_findTrafficManager_differentNamespace_present() {
	require := is.Require()
	ctx := is.initContext()
	oldCfg, err := clientcmd.LoadFromFile(is.kubeConfig)
	require.NoError(err)
	defer func() {
		is.NoError(clientcmd.WriteToFile(*oldCfg, is.kubeConfig))
	}()

	customNamespace := fmt.Sprintf("custom-%d", os.Getpid())
	_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "create", "namespace", customNamespace).Run()
	defer func() {
		_ = dexec.CommandContext(ctx, "kubectl", "--kubeconfig", is.kubeConfig, "delete", "namespace", customNamespace, "--wait=false").Run()
	}()

	// Load the config again so that oldCfg isn't disturbed.
	cfg, err := clientcmd.LoadFromFile(is.kubeConfig)
	require.NoError(err)
	require.NoError(api.MinifyConfig(cfg))
	var cluster *api.Cluster
	for _, c := range cfg.Clusters {
		cluster = c
		break
	}
	require.NotNil(cluster, "Unable to get cluster from config")
	cluster.Extensions = map[string]runtime.Object{"telepresence.io": &runtime.Unknown{
		Raw: []byte(fmt.Sprintf(`{"manager":{"namespace": "%s"}}`, customNamespace)),
	}}
	require.NoError(clientcmd.WriteToFile(*cfg, is.kubeConfig))
	is.findTrafficManagerPresent(customNamespace)
}

func (is *installSuite) Test_ensureTrafficManager_notPresent() {
	require := is.Require()
	ctx := is.initContext()
	defer is.removeManager(is.managerNamespace)
	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": is.kubeConfig, "namespace": is.namespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)
	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)
	require.NoError(ti.EnsureManager(ctx))
}

func (is *installSuite) initContext() context.Context {
	require := is.Require()
	ctx := dlog.NewTestContext(is.T(), false)

	env, err := client.LoadEnv(ctx)
	require.NoError(err)
	ctx = client.WithEnv(ctx, env)

	ctx, err = client.SetDefaultConfig(ctx, is.T().TempDir())
	require.NoError(err)
	return ctx
}

func (is *installSuite) findTrafficManagerPresent(namespace string) {
	require := is.Require()
	c := is.initContext()
	defer is.removeManager(namespace)

	cfgAndFlags, err := userd_k8s.NewConfig(c, map[string]string{"kubeconfig": is.kubeConfig, "namespace": namespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(c, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)
	watcherErr := make(chan error)
	watchCtx, watchCancel := context.WithCancel(c)
	defer func() {
		watchCancel()
		if err := <-watcherErr; err != nil {
			is.Fail(err.Error())
		}
	}()
	go func() {
		watcherErr <- kc.RunWatchers(watchCtx)
	}()
	waitCtx, waitCancel := context.WithTimeout(c, 10*time.Second)
	defer waitCancel()

	require.NoError(kc.WaitUntilReady(waitCtx))
	ti, err := NewTrafficManagerInstaller(kc)
	require.NoError(err)
	require.NoError(ti.EnsureManager(c))
	require.Eventually(func() bool {
		dep, err := ti.FindDeployment(c, namespace, install.ManagerAppName)
		v := strings.TrimPrefix(version.Version, "v")
		img := dep.Spec.Template.Spec.Containers[0].Image
		return err == nil && dep != nil && strings.Contains(img, v)
	}, 10*time.Second, 2*time.Second, "traffic-manager deployment not found")
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

	dirinfos, err := os.ReadDir("testdata/addAgentToWorkload")
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for _, di := range dirinfos {
		fileinfos, err := os.ReadDir(filepath.Join("testdata/addAgentToWorkload", di.Name()))
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
	ctx = client.WithEnv(ctx, env)
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx = client.WithConfig(ctx, cfg)

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
				env, err := client.LoadEnv(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ctx = client.WithEnv(ctx, env)
				ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
				cfg, err = client.LoadConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				ctx = client.WithConfig(ctx, cfg)

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

					err = os.WriteFile(
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
		if goRuntime.GOOS == "windows" && c.Name == "traffic-agent" {
			for j, v := range c.VolumeMounts {
				v.MountPath = filepath.Clean(v.MountPath)
				c.VolumeMounts[j] = v
			}
		}
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
