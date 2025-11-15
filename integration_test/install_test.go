package integration_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const ManagerAppName = agentconfig.ManagerAppName

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
	if !is.ManagerVersion().EQ(is.ClientVersion()) {
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

	kc := is.cluster(ctx, "", is.ManagerNamespace())
	helmConfig, err := getHelmConfig(kc, kc.Kubeconfig, is.ManagerNamespace())
	rq.NoError(err)

	getValues := func() (map[string]any, error) {
		return action.NewGetValues(helmConfig).Run(agentconfig.ManagerAppName)
	}
	containsKey := func(m map[string]any, key string) bool {
		_, ok := m[key]
		return ok
	}

	oldValues, err := getValues()
	rq.NoError(err)
	args := []string{"helm", "upgrade", "--namespace", is.ManagerNamespace()}
	if !is.ManagerVersion().EQ(version.Structured) {
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
	if !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests. PackageHelmChart assumes current version.")
	}
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
	values = append([]string{"template", agentconfig.ManagerAppName, chart, "-n", is.ManagerNamespace()}, values...)
	manifest, err := itest.Output(ctx, "helm", values...)
	require.NoError(err)
	out := dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
	logCtx := dos.WithStdout(dos.WithStderr(ctx, out), out)
	require.NoError(itest.Kubectl(dos.WithStdin(logCtx, strings.NewReader(manifest)), "", "apply", "-f", "-"))
	defer func() {
		// Sometimes the traffic-agents configmap gets wiped, causing the delete command to fail, hence we don't require.NoError
		_ = itest.Kubectl(dos.WithStdin(logCtx, strings.NewReader(manifest)), "", "delete", "-f", "-")
	}()
	require.NoError(itest.RolloutStatusWait(ctx, is.ManagerNamespace(), "deploy/"+agentconfig.ManagerAppName))
	is.CapturePodLogs(ctx, agentconfig.ManagerAppName, "", is.ManagerNamespace())
	stdout := is.TelepresenceConnect(ctx)
	is.Contains(stdout, "Connected to context")
	itest.TelepresenceQuitOk(ctx)
}

func (is *installSuite) Test_FindTrafficManager_notPresent() {
	kc := is.cluster(is.Context(), "", is.ManagerNamespace()) // ensure that k8sapi is initialized

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = sv }()

	_, err := k8sapi.GetDeployment(kc, ManagerAppName, is.ManagerNamespace())
	is.Error(err, "expected find to not find traffic-manager deployment")
}

func (is *installSuite) Test_EnsureManager_toleratesFailedInstall() {
	if !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests.")
	}
	require := is.Require()
	ctx := is.Context()

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	restoreVersion := func() { version.Version = sv }

	// We'll call this further down, but defer it to prevent polluting other tests if we don't leave this function gracefully
	defer restoreVersion()
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	failCtx := itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Timeouts().PrivateHelm = 20 * time.Second // Give it time to discover the ImagePullbackOff error
	})

	kc := is.cluster(failCtx, "", is.ManagerNamespace())
	err := ensureTrafficManager(kc)
	require.Error(err)
	dlog.Infof(ctx, "Got expected install failure: %v", err)
	restoreVersion()

	okCtx := itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Timeouts().PrivateHelm = 20 * time.Second // Time to wait before pending state makes us assume it's stuck.
	})
	kc = is.cluster(okCtx, "", is.ManagerNamespace())
	if !is.Eventually(func() bool {
		err = ensureTrafficManager(kc)
		if err != nil {
			dlog.Errorf(ctx, "ensureTrafficManager failed: %v", err)
		}
		return err == nil
	}, time.Minute, 5*time.Second) {
		is.Fail(fmt.Sprintf("Unable to install proper manager after failed install: %v", err))
	}
}

func (is *installSuite) Test_RemoveManager_canUninstall() {
	if !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests.")
	}
	require := is.Require()
	ctx := is.Context()
	kc := is.cluster(ctx, "", is.ManagerNamespace())

	require.NoError(ensureTrafficManager(kc))
	require.NoError(helm.DeleteTrafficManager(ctx, kc.Kubeconfig, k8s.GetManagerNamespace(kc), true, &helm.Request{}))
	// We want to make sure that we can re-install the manager after it's been uninstalled,
	// so try to ensureManager again.
	require.NoError(ensureTrafficManager(kc))
	// Uninstall the manager one last time -- this should behave the same way as the previous uninstall
	require.NoError(helm.DeleteTrafficManager(kc, kc.Kubeconfig, k8s.GetManagerNamespace(kc), true, &helm.Request{}))
}

func (is *installSuite) Test_No_Upgrade() {
	if !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests.")
	}
	ctx := is.Context()
	require := is.Require()
	kc := is.cluster(ctx, "", is.ManagerNamespace())

	defer is.UninstallTrafficManager(kc, is.ManagerNamespace())
	// first install
	require.NoError(ensureTrafficManager(kc))

	// errors and asks for telepresence upgrade
	require.Error(ensureTrafficManager(kc))

	// using upgrade and --values replaces TM with values
	helmValues := filepath.Join("testdata", "routing-values.yaml")
	opts := values.Options{ValueFiles: []string{helmValues}}
	vp, err := opts.MergeValues(getter.Providers{})
	require.NoError(err)
	jvp, err := json.Marshal(vp)
	require.NoError(err)

	require.NoError(helm.EnsureTrafficManager(kc, kc.Kubeconfig, k8s.GetManagerNamespace(kc), &helm.Request{
		Type:       helm.Upgrade,
		ValuesJson: jvp,
	}))
}

func (is *installSuite) Test_findTrafficManager_differentNamespace_present() {
	if !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests.")
	}
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
	kc := is.cluster(ctx, context, namespace)
	require := is.Require()
	require.NoError(ensureTrafficManager(kc))
	require.Eventually(func() bool {
		dep, err := k8sapi.GetDeployment(kc, ManagerAppName, namespace)
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

func (is *installSuite) cluster(ctx context.Context, context, managerNamespace string) *k8s.Cluster {
	cluster, err := is.GetK8SCluster(ctx, context, managerNamespace)
	is.Require().NoError(err)
	return cluster
}

func ensureTrafficManager(kc *k8s.Cluster) error {
	return helm.EnsureTrafficManager(
		kc,
		kc.Kubeconfig,
		k8s.GetManagerNamespace(kc),
		&helm.Request{Type: helm.Install})
}

func unTgz(ctx context.Context, srcTgz, dstPath string) error {
	rd, err := os.Open(srcTgz)
	if err != nil {
		return err
	}
	defer rd.Close()

	err = dos.MkdirAll(ctx, dstPath, 0o755)
	if err != nil {
		return err
	}

	zrd, err := gzip.NewReader(rd)
	if err != nil {
		return err
	}
	src := tar.NewReader(zrd)
	for {
		header, err := src.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		dst := dstPath + "/" + header.Name
		mode := os.FileMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			err = dos.MkdirAll(ctx, dst, mode)
			if err != nil {
				return err
			}
		case tar.TypeReg:
			err = dos.MkdirAll(ctx, filepath.Dir(dst), 0o755)
			if err != nil {
				return err
			}
			w, err := dos.OpenFile(ctx, dst, os.O_CREATE|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, src)
			_ = w.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unable to untar type : %c in file %s", header.Typeflag, header.Name)
		}
	}
	return nil
}

func (is *installSuite) Test_HelmSubChart() {
	if runtime.GOOS == "windows" || !(is.ManagerVersion().EQ(version.Structured) && is.ClientVersion().EQ(version.Structured)) {
		is.T().Skip("Not part of compatibility tests. Need forward slashes in path, and PackageHelmChart assumes current version.")
	}
	ctx := is.Context()
	require := is.Require()

	t := is.T()
	subChart, err := is.PackageHelmChart(ctx)
	require.NoError(err)

	base := t.TempDir()
	require.NoError(unTgz(ctx, subChart, filepath.Join(base, "charts")))

	chart := fmt.Sprintf(`apiVersion: v2
dependencies:
  - name: telepresence-oss
    registry: ../charts/telepresence-oss
    version: %s
    condition: enabled
description: Helm chart to deploy telepresence
name: parent
version: 1.0.0`, is.ClientVersion())

	vals := is.GetSetArgsForHelm(ctx, map[string]any{
		"global": map[string]any{
			"some-string": "value",
			"some-obj": map[string]any{
				"foo": "bar",
			},
			"some-bool": true,
		},
		"telepresence-oss": map[string]any{
			"clientRbac": map[string]any{
				"create": true,
				"subjects": []rbac.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      itest.TestUser,
						Namespace: is.ManagerNamespace(),
					},
				},
			},
		},
	}, false)
	require.NoError(dos.WriteFile(ctx, filepath.Join(base, "Chart.yaml"), []byte(chart), 0o644))

	vals = append([]string{"template", "parent", base, "-n", is.ManagerNamespace()}, vals...)
	so, err := itest.Output(ctx, "helm", vals...)
	require.NoError(err)
	require.Contains(so, "# Source: parent/charts/telepresence-oss/templates/clientRbac/connect.yaml")
	require.Contains(so, "name: "+itest.TestUser)
}
