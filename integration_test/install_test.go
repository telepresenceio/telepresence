package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

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

func (is *installSuite) Test_NonHelmInstall() {
	ctx := is.Context()
	require := is.Require()

	chart, err := is.PackageHelmChart(ctx)
	require.NoError(err)
	values := is.GetValuesForHelm(ctx, map[string]string{}, false)
	values = append([]string{"template", "traffic-manager", chart, "-n", is.ManagerNamespace()}, values...)
	manifest, err := itest.Output(ctx, "helm", values...)
	require.NoError(err)
	cmd := itest.Command(ctx, "kubectl", "--kubeconfig", itest.KubeConfig(ctx), "-n", is.ManagerNamespace(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out := dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(cmd.Run())
	defer func() {
		cmd := itest.Command(ctx, "kubectl", "--kubeconfig", itest.KubeConfig(ctx), "-n", is.ManagerNamespace(), "delete", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out := dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()
		cmd.Stdout = out
		cmd.Stderr = out
		// Sometimes the traffic-agents configmap gets wiped, causing the delete command to fail, hence we don't require.NoError
		_ = cmd.Run()
	}()
	stdout := itest.TelepresenceOk(itest.WithUser(ctx, "default"), "connect")
	is.Contains(stdout, "Connected to context")
	defer itest.TelepresenceQuitOk(ctx)
}

func (is *installSuite) Test_FindTrafficManager_notPresent() {
	ctx := is.Context()
	ctx, _ = is.cluster(ctx, "", is.ManagerNamespace()) // ensure that k8sapi is initialized

	sv := version.Version
	version.Version = "v0.0.0-bogus"
	defer func() { version.Version = sv }()

	_, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
	is.Error(err, "expected find to not find traffic-manager deployment")
}

func (is *installSuite) Test_EnsureManager_updateFromLegacy() {
	require := is.Require()
	ctx := is.Context()

	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	f, err := os.ReadFile("testdata/legacyManifests/manifests.yml")
	require.NoError(err)
	manifest := string(f)
	ca, crt, key, err := certsetup(is.ManagerNamespace())
	require.NoError(err)
	manifest = strings.ReplaceAll(manifest, "{{.ManagerNamespace}}", is.ManagerNamespace())
	manifest = strings.ReplaceAll(manifest, "{{.CA}}", base64.StdEncoding.EncodeToString(ca))
	manifest = strings.ReplaceAll(manifest, "{{.CRT}}", base64.StdEncoding.EncodeToString(crt))
	manifest = strings.ReplaceAll(manifest, "{{.KEY}}", base64.StdEncoding.EncodeToString(key))

	cmd := itest.Command(ctx, "kubectl", "--kubeconfig", itest.KubeConfig(ctx), "-n", is.ManagerNamespace(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	out := dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(cmd.Run())
	require.NoError(itest.Kubectl(ctx, is.ManagerNamespace(), "rollout", "status", "-w", "deploy/traffic-manager"))

	is.findTrafficManagerPresent(ctx, "", is.ManagerNamespace())
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

	ctx = itest.WithConfig(ctx, func(cfg *client.Config) {
		cfg.Timeouts.PrivateHelm = 30 * time.Second
	})
	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())
	require.Error(ensureTrafficManager(ctx, kc))
	restoreVersion()
	var err error
	require.Eventually(func() bool {
		err = ensureTrafficManager(ctx, kc)
		return err == nil
	}, 3*time.Minute, 5*time.Second, "Unable to install proper manager after failed install: %v", err)
}

func certsetup(namespace string) ([]byte, []byte, []byte, error) {
	// Most of this is adapted from https://gist.github.com/shaneutt/5e1995295cff6721c89a71d13a71c251
	// set up our CA certificate
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization: []string{"getambassador.io"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	// create our private and public key
	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, nil, err
	}

	// create the CA
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, nil, err
	}

	// pem encode
	caPEM := new(bytes.Buffer)
	err = pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	caPrivKeyPEM := new(bytes.Buffer)
	err = pem.Encode(caPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey),
	})
	if err != nil {
		return nil, nil, nil, err
	}

	// set up our server certificate
	host := fmt.Sprintf("agent-injector.%s", namespace)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization: []string{"getambassador.io"},
			CommonName:   host,
		},
		DNSNames:    []string{host},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(10, 0, 0),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}

	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, nil, err
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, nil, err
	}

	certPEM := new(bytes.Buffer)
	err = pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	certPrivKeyPEM := new(bytes.Buffer)
	err = pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return caPEM.Bytes(), certPEM.Bytes(), certPrivKeyPEM.Bytes(), nil
}

func (is *installSuite) Test_EnsureManager_toleratesLeftoverState() {
	require := is.Require()
	ctx := is.Context()

	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())
	require.NoError(ensureTrafficManager(ctx, kc))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	is.UninstallTrafficManager(ctx, is.ManagerNamespace())
	require.NoError(ensureTrafficManager(ctx, kc))
	require.Eventually(func() bool {
		obj, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
		if err != nil {
			return false
		}
		deploy, _ := k8sapi.DeploymentImpl(obj)
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 10*time.Second, time.Second, "timeout waiting for deployment to update")
}

func (is *installSuite) Test_RemoveManager_canUninstall() {
	require := is.Require()
	ctx := is.Context()
	ctx, kc := is.cluster(ctx, "", is.ManagerNamespace())

	require.NoError(ensureTrafficManager(ctx, kc))
	require.NoError(helm.DeleteTrafficManager(ctx, kc.ConfigFlags, kc.GetManagerNamespace(), true, &connector.HelmRequest{}))
	// We want to make sure that we can re-install the manager after it's been uninstalled,
	// so try to ensureManager again.
	require.NoError(ensureTrafficManager(ctx, kc))
	// Uninstall the manager one last time -- this should behave the same way as the previous uninstall
	require.NoError(helm.DeleteTrafficManager(ctx, kc.ConfigFlags, kc.GetManagerNamespace(), true, &connector.HelmRequest{}))
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
		obj, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
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

	require.NoError(helm.EnsureTrafficManager(ctx, kc.ConfigFlags, kc.GetManagerNamespace(), &connector.HelmRequest{
		Type:       connector.HelmRequest_UPGRADE,
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
		dep, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, namespace)
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
		kc.ConfigFlags,
		kc.GetManagerNamespace(),
		&connector.HelmRequest{Type: connector.HelmRequest_INSTALL})
}
