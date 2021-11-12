package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type installSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddNamespacePairSuite("-auto-install", func(h itest.NamespacePair) suite.TestingSuite {
		return &installSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (is *installSuite) Test_FindTrafficManager_notPresent() {
	ctx := is.Context()
	ti := is.installer(ctx)

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = sv }()

	_, err := ti.FindDeployment(ctx, is.ManagerNamespace(), install.ManagerAppName)
	is.Error(err, "expected find to not find traffic-manager deployment")
}

func (is *installSuite) Test_EnsureManager_updateFromLegacy() {
	require := is.Require()
	ctx := is.Context()

	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	f, err := os.ReadFile("testdata/legacyManifests/manifests.yml")
	require.NoError(err)
	manifest := string(f)
	manifest = strings.ReplaceAll(manifest, "{{.ManagerNamespace}}", is.ManagerNamespace())
	cmd := itest.Command(ctx, "kubectl", "--kubeconfig", itest.KubeConfig(ctx), "-n", is.ManagerNamespace(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out := dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(cmd.Run())
	require.NoError(itest.Kubectl(ctx, is.ManagerNamespace(), "rollout", "status", "-w", "deploy/traffic-manager"))

	is.findTrafficManagerPresent(ctx, is.ManagerNamespace())
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

	ctx = itest.WithConfig(ctx, &client.Config{
		Timeouts: client.Timeouts{
			PrivateHelm: 30 * time.Second,
		},
	})
	ti := is.installer(ctx)
	require.Error(ti.EnsureManager(ctx))
	restoreVersion()

	var err error
	require.Eventually(func() bool {
		err = ti.EnsureManager(ctx)
		return err == nil
	}, 20*time.Second, 5*time.Second, "Unable to install proper manager after failed install: %v", err)
}

func (is *installSuite) Test_EnsureManager_toleratesLeftoverState() {
	require := is.Require()
	ctx := is.Context()

	ti := is.installer(ctx)
	require.NoError(ti.EnsureManager(ctx))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	is.UninstallTrafficManager(ctx, is.ManagerNamespace())
	require.NoError(ti.EnsureManager(ctx))
	require.Eventually(func() bool {
		deploy, err := ti.FindDeployment(ctx, is.ManagerNamespace(), install.ManagerAppName)
		if err != nil {
			return false
		}
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 10*time.Second, time.Second, "timeout waiting for deployment to update")
}

func (is *installSuite) Test_RemoveManagerAndAgents_canUninstall() {
	require := is.Require()
	ctx := is.Context()
	ti := is.installer(ctx)

	require.NoError(ti.EnsureManager(ctx))
	require.NoError(ti.RemoveManagerAndAgents(ctx, false, []*manager.AgentInfo{}))
	// We want to make sure that we can re-install the agent after it's been uninstalled,
	// so try to ensureManager again.
	require.NoError(ti.EnsureManager(ctx))
	// Uninstall the agent one last time -- this should behave the same way as the previous uninstall
	require.NoError(ti.RemoveManagerAndAgents(ctx, false, []*manager.AgentInfo{}))
}

func (is *installSuite) Test_EnsureManager_upgrades() {
	// TODO: In order to properly check that an upgrade works, we need to install
	//  an older version first, which in turn will entail building that version
	//  and publishing an image fore it. The way the test looks right now, it just
	//  terminates with a timeout error.
	is.T().Skip()
	require := is.Require()
	ctx := is.Context()
	ti := is.installer(ctx)

	require.NoError(ti.EnsureManager(ctx))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	sv := version.Version
	version.Version = "v3.0.0-bogus"
	restoreVersion := func() { version.Version = sv }
	defer restoreVersion()
	require.Error(ti.EnsureManager(ctx))

	require.Eventually(func() bool {
		deploy, err := ti.FindDeployment(ctx, is.ManagerNamespace(), install.ManagerAppName)
		if err != nil {
			return false
		}
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 30*time.Second, 5*time.Second, "timeout waiting for deployment to update")

	restoreVersion()
	require.NoError(ti.EnsureManager(ctx))
}

func (is *installSuite) Test_EnsureManager_doesNotChangeExistingHelm() {
	require := is.Require()
	ctx := is.Context()

	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{"kubeconfig": itest.KubeConfig(ctx), "namespace": is.ManagerNamespace()})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)

	// The helm chart is declared as 1.9.9 to make sure it's "older" than ours, but we set the tag to 2.4.0 so that it actually starts up.
	// 2.4.0 was the latest release at the time that testdata/telepresence-1.9.9.tgz was packaged
	tgzFile := filepath.Join(itest.GetWorkingDir(ctx), "testdata", "telepresence-1.9.9.tgz")
	err = itest.Run(itest.WithModuleRoot(ctx),
		"tools/bin/helm",
		"--kubeconfig", itest.KubeConfig(ctx),
		"-n", is.ManagerNamespace(),
		"install", "traffic-manager", tgzFile,
		"--create-namespace",
		"--atomic",
		"--set", "clusterID="+kc.GetClusterId(ctx),
		"--set", "image.tag=2.4.0",
		"--wait",
	)
	require.NoError(err)

	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	ti := is.installer(ctx)

	require.NoError(ti.EnsureManager(ctx))

	kc.Client().InvalidateCache()
	dep, err := ti.FindDeployment(ctx, is.ManagerNamespace(), install.ManagerAppName)
	require.NoError(err)
	require.NotNil(dep)
	require.Contains(dep.Spec.Template.Spec.Containers[0].Image, "2.4.0")
	require.Equal(dep.Labels["helm.sh/chart"], "telepresence-1.9.9")
}

func (is *installSuite) Test_findTrafficManager_differentNamespace_present() {
	ctx := is.Context()
	customNamespace := fmt.Sprintf("custom-%d", os.Getpid())
	itest.CreateNamespaces(ctx, customNamespace)
	defer itest.DeleteNamespaces(ctx, customNamespace)
	ctx = itest.WithEnv(ctx, map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": customNamespace})
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]interface{} {
		return map[string]interface{}{"manager": map[string]string{"namespace": customNamespace}}
	})
	is.findTrafficManagerPresent(ctx, customNamespace)
}

func (is *installSuite) findTrafficManagerPresent(ctx context.Context, namespace string) {
	kc := is.cluster(ctx, namespace)
	watcherErr := make(chan error)
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer func() {
		watchCancel()
		if err := <-watcherErr; err != nil {
			is.Fail(err.Error())
		}
	}()
	go func() {
		watcherErr <- kc.RunWatchers(watchCtx)
	}()
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()

	require := is.Require()
	require.NoError(kc.WaitUntilReady(waitCtx))
	ti, err := userd_trafficmgr.NewTrafficManagerInstaller(kc)
	require.NoError(err)
	require.NoError(ti.EnsureManager(ctx))
	require.Eventually(func() bool {
		dep, err := ti.FindDeployment(ctx, namespace, install.ManagerAppName)
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		v := strings.TrimPrefix(version.Version, "v")
		img := dep.Spec.Template.Spec.Containers[0].Image
		dlog.Infof(ctx, "traffic-manager image %s, our version %s", img, v)
		return strings.Contains(img, v)
	}, 10*time.Second, 2*time.Second, "traffic-manager deployment not found")
}

func (is *installSuite) cluster(ctx context.Context, managerNamespace string) *userd_k8s.Cluster {
	require := is.Require()
	cfgAndFlags, err := userd_k8s.NewConfig(ctx, map[string]string{
		"kubeconfig": itest.KubeConfig(ctx),
		"context":    "default",
		"namespace":  managerNamespace})
	require.NoError(err)
	kc, err := userd_k8s.NewCluster(ctx, cfgAndFlags, nil, userd_k8s.Callbacks{})
	require.NoError(err)
	return kc
}

func (is *installSuite) installer(ctx context.Context) userd_trafficmgr.Installer {
	ti, err := userd_trafficmgr.NewTrafficManagerInstaller(is.cluster(ctx, is.ManagerNamespace()))
	is.Require().NoError(err)
	return ti
}
