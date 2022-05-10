package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
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
	ctx, _ = is.installer(ctx)

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
	ctx, ti := is.installer(ctx)
	require.Error(ti.EnsureManager(ctx))
	restoreVersion()

	var err error
	require.Eventually(func() bool {
		err = ti.EnsureManager(ctx)
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

	ctx, ti := is.installer(ctx)
	require.NoError(ti.EnsureManager(ctx))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	is.UninstallTrafficManager(ctx, is.ManagerNamespace())
	require.NoError(ti.EnsureManager(ctx))
	require.Eventually(func() bool {
		obj, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
		if err != nil {
			return false
		}
		deploy, _ := k8sapi.DeploymentImpl(obj)
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 10*time.Second, time.Second, "timeout waiting for deployment to update")
}

func (is *installSuite) Test_RemoveManagerAndAgents_canUninstall() {
	require := is.Require()
	ctx := is.Context()
	ctx, ti := is.installer(ctx)

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
	ctx, ti := is.installer(ctx)

	require.NoError(ti.EnsureManager(ctx))
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	sv := version.Version
	version.Version = "v3.0.0-bogus"
	restoreVersion := func() { version.Version = sv }
	defer restoreVersion()
	require.Error(ti.EnsureManager(ctx))

	require.Eventually(func() bool {
		obj, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
		if err != nil {
			return false
		}
		deploy, _ := k8sapi.DeploymentImpl(obj)
		return deploy.Status.ReadyReplicas == int32(1) && deploy.Status.Replicas == int32(1)
	}, 30*time.Second, 5*time.Second, "timeout waiting for deployment to update")

	restoreVersion()
	require.NoError(ti.EnsureManager(ctx))
}

func (is *installSuite) Test_EnsureManager_doesNotChangeExistingHelm() {
	require := is.Require()
	ctx := is.Context()

	cfgAndFlags, err := k8s.NewConfig(ctx, map[string]string{"kubeconfig": itest.KubeConfig(ctx), "namespace": is.ManagerNamespace()})
	require.NoError(err)
	kc, err := k8s.NewCluster(ctx, cfgAndFlags, nil)
	ctx = kc.WithK8sInterface(ctx)
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

	ctx, ti := is.installer(ctx)

	require.NoError(ti.EnsureManager(ctx))

	dep, err := k8sapi.GetDeployment(ctx, install.ManagerAppName, is.ManagerNamespace())
	require.NoError(err)
	require.NotNil(dep)
	require.Contains(dep.GetPodTemplate().Spec.Containers[0].Image, "2.4.0")
	require.Equal(dep.GetLabels()["helm.sh/chart"], "telepresence-1.9.9")
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
	ctx, kc := is.cluster(ctx, namespace)
	require := is.Require()
	ti, err := trafficmgr.NewTrafficManagerInstaller(kc)
	require.NoError(err)
	require.NoError(ti.EnsureManager(ctx))
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

func (is *installSuite) cluster(ctx context.Context, managerNamespace string) (context.Context, *k8s.Cluster) {
	require := is.Require()
	cfgAndFlags, err := k8s.NewConfig(ctx, map[string]string{
		"kubeconfig": itest.KubeConfig(ctx),
		"context":    "default",
		"namespace":  managerNamespace})
	require.NoError(err)
	kc, err := k8s.NewCluster(ctx, cfgAndFlags, nil)
	require.NoError(err)
	return kc.WithK8sInterface(ctx), kc
}

func (is *installSuite) installer(ctx context.Context) (context.Context, trafficmgr.Installer) {
	ctx, kc := is.cluster(ctx, is.ManagerNamespace())
	ti, err := trafficmgr.NewTrafficManagerInstaller(kc)
	is.Require().NoError(err)
	return ctx, ti
}
