package trafficmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

const rootKubeconfigTemplate = `
apiVersion: v1
kind: Config
current-context: plain
contexts:
- name: plain
  context: {cluster: cl, user: cert-user}
- name: other
  context: {cluster: cl, user: plugin-user}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:6443", certificate-authority: %s}
users:
- name: cert-user
  user: {client-certificate: %s, client-key: %s}
- name: plugin-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      interactiveMode: Never
      command: gke-auth
- name: token-file-user
  user: {tokenFile: %s}
`

func newRootKubeconfigTestConfig(t *testing.T, flags map[string]string) *k8s.Kubeconfig {
	t.Helper()
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
		return p
	}
	path := write("config", fmt.Sprintf(rootKubeconfigTemplate,
		write("ca.crt", "KUBECONFIG-CA"), write("client.crt", "CERT"), write("client.key", "KEY"), write("token", "file-token\n")))
	flags["kubeconfig"] = path
	configFlags, err := k8s.ConfigFlags(flags)
	require.NoError(t, err)
	return &k8s.Kubeconfig{ClientConfig: configFlags.ToRawKubeConfigLoader(), EffectiveFlagMap: flags}
}

func TestRootKubeconfig_namesNoFile(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	kc := newRootKubeconfigTestConfig(t, map[string]string{})
	data, creds, err := rootKubeconfig(ctx, kc, "")
	require.NoError(t, err)
	assert.Nil(t, creds)
	require.NoError(t, k8s.CheckRootRequest(nil, data))

	cfg, err := clientcmd.Load(data)
	require.NoError(t, err)
	assert.Equal(t, []byte("KUBECONFIG-CA"), cfg.Clusters["cl"].CertificateAuthorityData)
	assert.Equal(t, []byte("CERT"), cfg.AuthInfos["cert-user"].ClientCertificateData)
	assert.Equal(t, []byte("KEY"), cfg.AuthInfos["cert-user"].ClientKeyData)
}

func TestRootKubeconfig_flagOverridesAreMerged(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	overrideCA := filepath.Join(t.TempDir(), "override-ca.crt")
	require.NoError(t, os.WriteFile(overrideCA, []byte("FLAG-CA"), 0o600))
	kc := newRootKubeconfigTestConfig(t, map[string]string{
		"server":                "https://flag.example.com:6443",
		"token":                 "flag-token",
		"as":                    "someone",
		"as-group":              "g1,g2",
		"certificate-authority": overrideCA,
		"tls-server-name":       "tls.example.com",
		"namespace":             "flag-ns",
	})
	data, creds, err := rootKubeconfig(ctx, kc, "")
	require.NoError(t, err)
	assert.Nil(t, creds)
	require.NoError(t, k8s.CheckRootRequest(nil, data))

	cfg, err := clientcmd.Load(data)
	require.NoError(t, err)
	cl := cfg.Clusters["cl"]
	assert.Equal(t, "https://flag.example.com:6443", cl.Server)
	assert.Equal(t, "tls.example.com", cl.TLSServerName)
	assert.Equal(t, []byte("FLAG-CA"), cl.CertificateAuthorityData)
	assert.Equal(t, "flag-ns", cfg.Contexts["plain"].Namespace)
	ai := cfg.AuthInfos["cert-user"]
	assert.Equal(t, "flag-token", ai.Token)
	assert.Equal(t, "someone", ai.Impersonate)
	assert.Equal(t, []string{"g1", "g2"}, ai.ImpersonateGroups)
}

func TestRootKubeconfig_pluginComesFromTheMergedResult(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	kc := newRootKubeconfigTestConfig(t, map[string]string{"user": "plugin-user"})
	data, creds, err := rootKubeconfig(ctx, kc, "")
	require.NoError(t, err)
	require.NotNil(t, creds)
	assert.Equal(t, "gke-auth", creds.execConfig.Command)
	require.NoError(t, k8s.CheckRootRequest(nil, data))

	kc = newRootKubeconfigTestConfig(t, map[string]string{"context": "other"})
	data, creds, err = rootKubeconfig(ctx, kc, "")
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.NoError(t, k8s.CheckRootRequest(nil, data))
}

func TestRootKubeconfig_tokenFileBecomesCredentials(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	kc := newRootKubeconfigTestConfig(t, map[string]string{"user": "token-file-user"})
	data, creds, err := rootKubeconfig(ctx, kc, "")
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.NoError(t, k8s.CheckRootRequest(nil, data))

	cred, err := creds.credential(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, `{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","status":{"token":"file-token"}}`, string(cred))
}

func TestTokenFileCredential_errors(t *testing.T) {
	_, err := tokenFileCredential(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)

	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, []byte(" \n"), 0o600))
	_, err = tokenFileCredential(empty)
	require.ErrorContains(t, err, "empty")
}

func TestGetNetworkInfo_rootd(t *testing.T) {
	ctx := client.WithConfig(testutil.NewContext(t, false), client.GetDefaultConfig())
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	const pem = "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n"
	require.NoError(t, os.WriteFile(caFile, []byte(pem), 0o600))
	cfg := client.GetConfig(ctx)
	cfg.Cluster().ManagerServerCA = caFile
	ctx = client.WithConfig(ctx, cfg)

	s := newCredentialTestSession(ctx, "sess", nil, nil)
	cr := &connector.ConnectRequest{KubeFlags: map[string]string{
		"kubeconfig":      "/etc/kube",
		"client-key":      "/etc/key",
		"request-timeout": "15s",
	}}

	nc, err := s.getNetworkInfo(cr, true)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"request-timeout": "15s"}, nc.KubeFlags)
	rootCfg, err := client.UnmarshalJSONConfig(nc.ClientConfig, false)
	require.NoError(t, err)
	assert.Equal(t, pem, rootCfg.Cluster().ManagerServerCA)
	// The session's own configuration is unchanged.
	assert.Equal(t, caFile, client.GetConfig(s).Cluster().ManagerServerCA)

	nc, err = s.getNetworkInfo(cr, false)
	require.NoError(t, err)
	assert.Equal(t, cr.KubeFlags, nc.KubeFlags)
	rootCfg, err = client.UnmarshalJSONConfig(nc.ClientConfig, false)
	require.NoError(t, err)
	assert.Equal(t, caFile, rootCfg.Cluster().ManagerServerCA)
}
