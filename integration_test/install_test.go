package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const ManagerAppName = agentmap.ManagerAppName

type installSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (is *installSuite) SuiteName() string {
	return "Install"
}

func init() {
	itest.AddNamespacePairSuite("-install", func(h itest.NamespacePair) itest.TestingSuite {
		return &installSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func getHelmConfig(ctx context.Context, clientGetter genericclioptions.RESTClientGetter, namespace string) (*action.Configuration, error) {
	helmConfig := &action.Configuration{}
	err := helmConfig.Init(clientGetter, namespace, "secrets", func(format string, args ...any) {
		ctx := dlog.WithField(ctx, "source", "helm")
		dlog.Infof(ctx, format, args...)
	})
	if err != nil {
		return nil, err
	}
	return helmConfig, nil
}

func (is *installSuite) AmendSuiteContext(ctx context.Context) context.Context {
	if !(is.ManagerVersion().EQ(is.ClientVersion()) || is.ClientIsVersion(">2.21.x")) {
		// Need to use the built executable because the client version doesn't handle the --version flag.
		exe, _ := is.Executable()
		ctx = itest.WithExecutable(ctx, exe)
	}
	return ctx
}

func (is *installSuite) Test_UpgradeRetainsValues() {
	ctx := is.Context()
	rq := is.Require()
	is.TelepresenceHelmInstallOK(ctx, false, "--set", "logLevel=debug")
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())
	helmConfig, err := getHelmConfig(ctx, kc.Kubeconfig, is.ManagerNamespace())
	rq.NoError(err)

	getValues := func() (map[string]any, error) {
		return action.NewGetValues(helmConfig).Run(agentmap.ManagerAppName)
	}
	containsKey := func(m map[string]any, key string) bool {
		_, ok := m[key]
		return ok
	}

	oldValues, err := getValues()
	rq.NoError(err)
	args := []string{"helm", "upgrade", "--namespace", is.ManagerNamespace()}
	if !(is.ManagerVersion().EQ(is.ClientVersion()) || is.ManagerVersion().LT(version.Structured)) {
		args = append(args, "--version", is.ManagerVersion().String())
	}

	is.Run("default reuse-values", func() {
		itest.TelepresenceOk(is.Context(), args...)
		newValues, err := getValues()
		if is.NoError(err) {
			is.Equal(oldValues, newValues)
		}
	})

	is.Run("default reset-values", func() {
		// Setting a value means that the default behavior is to reset old values.
		itest.TelepresenceOk(is.Context(), append(args, "--set", "apiPort=8765")...)
		newValues, err := getValues()
		if is.NoError(err) {
			is.Equal(8765.0, newValues["apiPort"])
			is.False(containsKey(newValues, "logLevel")) // Should be back at default
		}
	})

	is.Run("explicit reuse-values", func() {
		// Set new value and enforce merge with of old values.
		itest.TelepresenceOk(is.Context(), append(args, "--set", "logLevel=debug", "--reuse-values")...)
		newValues, err := getValues()
		if is.NoError(err) {
			is.Equal(8765.0, newValues["apiPort"])
			is.Equal("debug", newValues["logLevel"])
		}
	})

	is.Run("explicit reset-values", func() {
		// Enforce reset of old values.
		itest.TelepresenceOk(is.Context(), append(args, "--reset-values")...)
		newValues, err := getValues()
		if is.NoError(err) {
			is.False(containsKey(newValues, "apiPort"))  // Should be back at default
			is.False(containsKey(newValues, "logLevel")) // Should be back at default
		}
	})
}

func (is *installSuite) Test_HelmTemplateInstall() {
	ctx := is.Context()
	require := is.Require()

	chart, err := is.PackageHelmChart(ctx)
	require.NoError(err)
	values := is.GetSetArgsForHelm(ctx, map[string]any{
		"clientRbac.create": true,
		"clientRbac.subjects": []rbac.Subject{{
			Kind:      "ServiceAccount",
			Name:      itest.TestUser,
			Namespace: is.ManagerNamespace(),
		}},
		"managerRbac.create": true,
	}, false)
	require.NoError(err)
	values = append([]string{"template", agentmap.ManagerAppName, chart, "-n", is.ManagerNamespace()}, values...)
	manifest, err := itest.Output(ctx, "helm", values...)
	require.NoError(err)
	out := dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
	logCtx := dos.WithStdout(dos.WithStderr(ctx, out), out)
	require.NoError(itest.Kubectl(dos.WithStdin(logCtx, strings.NewReader(manifest)), "", "apply", "-f", "-"))
	defer func() {
		// Sometimes the traffic-agents configmap gets wiped, causing the delete command to fail, hence we don't require.NoError
		_ = itest.Kubectl(dos.WithStdin(logCtx, strings.NewReader(manifest)), "", "delete", "-f", "-")
	}()
	require.NoError(itest.RolloutStatusWait(ctx, is.ManagerNamespace(), "deploy/"+agentmap.ManagerAppName))
	is.CapturePodLogs(ctx, agentmap.ManagerAppName, "", is.ManagerNamespace())
	stdout := is.TelepresenceConnect(ctx)
	is.Contains(stdout, "Connected to context")
	itest.TelepresenceQuitOk(ctx)
}

func (is *installSuite) Test_FindTrafficManager_notPresent() {
	ctx := is.Context()
	ctx, _ = is.cluster(ctx, "", is.ManagerNamespace()) // ensure that k8sapi is initialized

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = sv }()

	_, err := k8sapi.GetDeployment(ctx, ManagerAppName, is.ManagerNamespace())
	is.Error(err, "expected find to not find traffic-manager deployment")
}

func (is *installSuite) Test_EnsureManager_toleratesFailedInstall() {
	require := is.Require()
	ctx := is.Context()

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	restoreVersion := func() { version.Version = sv }

	// We'll call this further down, but defer it to prevent polluting other tests if we don't leave this function gracefully
	defer restoreVersion()
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())

	failCtx := itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Timeouts().PrivateHelm = 20 * time.Second // Give it time to discover the ImagePullbackOff error
	})

	err := ensureTrafficManager(failCtx, kc)
	require.Error(err)
	dlog.Infof(ctx, "Got expected install failure: %v", err)
	restoreVersion()

	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Timeouts().PrivateHelm = 20 * time.Second // Time to wait before pending state makes us assume it's stuck.
	})
	if !is.Eventually(func() bool {
		err = ensureTrafficManager(ctx, kc)
		if err != nil {
			dlog.Errorf(ctx, "ensureTrafficManager failed: %v", err)
		}
		return err == nil
	}, time.Minute, 5*time.Second) {
		is.Fail(fmt.Sprintf("Unable to install proper manager after failed install: %v", err))
	}
}

func (is *installSuite) Test_RemoveManager_canUninstall() {
	require := is.Require()
	ctx := is.Context()
	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())

	require.NoError(ensureTrafficManager(ctx, kc))
	require.NoError(helm.DeleteTrafficManager(ctx, kc.Kubeconfig, k8s.GetManagerNamespace(ctx), true, &helm.Request{}))
	// We want to make sure that we can re-install the manager after it's been uninstalled,
	// so try to ensureManager again.
	require.NoError(ensureTrafficManager(ctx, kc))
	// Uninstall the manager one last time -- this should behave the same way as the previous uninstall
	require.NoError(helm.DeleteTrafficManager(ctx, kc.Kubeconfig, k8s.GetManagerNamespace(ctx), true, &helm.Request{}))
}

func (is *installSuite) Test_EnsureManager_upgrades_and_values() {
	// TODO: In order to properly check that an upgrade works, we need to install
	//  an older version first, which in turn will entail building that version
	//  and publishing an image fore it. The way the test looks right now, it just
	//  terminates with a timeout error.
	is.T().Skip()
	require := is.Require()
	ctx := is.Context()
	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())
	require.NoError(ensureTrafficManager(ctx, kc))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	sv := version.Version
	version.Version = "v3.0.0-bogus"
	restoreVersion := func() { version.Version = sv }
	defer restoreVersion()
	require.Error(ensureTrafficManager(ctx, kc))

	require.Eventually(func() bool {
		obj, err := k8sapi.GetDeployment(ctx, ManagerAppName, is.ManagerNamespace())
		if err != nil {
			return false
		}
		deploy, _ := k8sapi.DeploymentImpl(obj)
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 30*time.Second, 5*time.Second, "timeout waiting for deployment to update")

	restoreVersion()
	require.NoError(ensureTrafficManager(ctx, kc))
}

func (is *installSuite) Test_No_Upgrade() {
	ctx := is.Context()
	require := is.Require()
	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())

	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())
	// first install
	require.NoError(ensureTrafficManager(ctx, kc))

	// errors and asks for telepresence upgrade
	require.Error(ensureTrafficManager(ctx, kc))

	// using upgrade and --values replaces TM with values
	helmValues := filepath.Join("testdata", "routing-values.yaml")
	opts := values.Options{ValueFiles: []string{helmValues}}
	vp, err := opts.MergeValues(getter.Providers{})
	require.NoError(err)
	jvp, err := json.Marshal(vp)
	require.NoError(err)

	require.NoError(helm.EnsureTrafficManager(ctx, kc.Kubeconfig, k8s.GetManagerNamespace(ctx), &helm.Request{
		Type:       helm.Upgrade,
		ValuesJson: jvp,
	}))
}

func (is *installSuite) Test_findTrafficManager_differentNamespace_present() {
	ctx := is.Context()
	customNamespace := fmt.Sprintf("custom-%d", os.Getpid())
	itest.CreateNamespaces(ctx, customNamespace)
	defer itest.DeleteNamespaces(ctx, customNamespace)
	defer is.UninstallTrafficManager(ctx, customNamespace)
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"manager": map[string]string{"namespace": customNamespace}}
	})
	is.findTrafficManagerPresent(ctx, "extra", customNamespace)
}

func (is *installSuite) findTrafficManagerPresent(ctx context.Context, context, namespace string) {
	ctx, kc := is.cluster(ctx, context, namespace)
	require := is.Require()
	require.NoError(ensureTrafficManager(ctx, kc))
	require.Eventually(func() bool {
		dep, err := k8sapi.GetDeployment(ctx, ManagerAppName, namespace)
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		v := strings.TrimPrefix(version.Version, "v")
		img := dep.GetPodTemplate().Spec.Containers[0].Image
		dlog.Infof(ctx, "traffic-manager image %s, our version %s", img, v)
		return strings.Contains(img, v)
	}, 10*time.Second, 2*time.Second, "traffic-manager deployment not found")
}

func (is *installSuite) cluster(ctx context.Context, context, managerNamespace string) (context.Context, *k8s.Cluster) {
	ctx, cluster, err := is.GetK8SCluster(ctx, context, managerNamespace)
	is.Require().NoError(err)
	return ctx, cluster
}

func ensureTrafficManager(ctx context.Context, kc *k8s.Cluster) error {
	return helm.EnsureTrafficManager(
		ctx,
		kc.Kubeconfig,
		k8s.GetManagerNamespace(ctx),
		&helm.Request{Type: helm.Install})
}
