package patcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func execConfig() *clientcmdapi.Config {
	cfg := clientcmdapi.NewConfig()
	cfg.Contexts["ctx"] = &clientcmdapi.Context{AuthInfo: "user"}
	cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "gke-auth"}}
	return cfg
}

func TestReplaceAuthExecWithStub(t *testing.T) {
	cfg := execConfig()
	require.NoError(t, replaceAuthExecWithStub(cfg, "telepresence", "host:1234", "/cfg/config.yml", "s3cret"))
	exec := cfg.AuthInfos["user"].Exec
	assert.Equal(t, "telepresence", exec.Command)
	assert.Equal(t, []string{"kubeauth", "--config", "/cfg/config.yml", "--token", "s3cret", "ctx", "host:1234"}, exec.Args)
}

func TestReplaceAuthExecWithStub_noToken(t *testing.T) {
	cfg := execConfig()
	require.NoError(t, replaceAuthExecWithStub(cfg, "telepresence", "host:1234", "/cfg/config.yml", ""))
	assert.Equal(t, []string{"kubeauth", "--config", "/cfg/config.yml", "ctx", "host:1234"}, cfg.AuthInfos["user"].Exec.Args)
}

const withoutExecKubeconfig = `
apiVersion: v1
kind: Config
current-context: plugin
contexts:
- name: plugin
  context: {cluster: cl, user: plugin-user}
- name: static
  context: {cluster: cl, user: static-user}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:6443", certificate-authority: %s}
users:
- name: plugin-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      interactiveMode: Never
      command: gke-auth
      args: [--project, p]
- name: static-user
  user:
    token: static-token
`

func loadTestPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	require.NoError(t, os.WriteFile(ca, []byte("CA-DATA"), 0o600))
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, withoutExecKubeconfig, ca), 0o600))
	return path
}

// loadOverridingConfig returns a loader for the test kubeconfig file, with the given overrides.
func loadOverridingConfig(t *testing.T, path string, overrides *clientcmd.ConfigOverrides) clientcmd.OverridingClientConfig {
	t.Helper()
	raw, err := (&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}).Load()
	require.NoError(t, err)
	return clientcmd.NewNonInteractiveClientConfig(*raw, overrides.CurrentContext, overrides, nil)
}

func loadTestConfig(t *testing.T) clientcmd.OverridingClientConfig {
	t.Helper()
	return loadOverridingConfig(t, loadTestPath(t), &clientcmd.ConfigOverrides{})
}

func TestCreateRootKubeConfig_removesExec(t *testing.T) {
	rc, err := CreateRootKubeConfig(t.Context(), loadTestConfig(t), "")
	require.NoError(t, err)

	ec := rc.Exec
	require.NotNil(t, ec)
	assert.Equal(t, "gke-auth", ec.Command)
	assert.Equal(t, []string{"--project", "p"}, ec.Args)
	assert.Equal(t, "client.authentication.k8s.io/v1", ec.APIVersion)
	assert.Empty(t, rc.TokenFile)

	cfg, err := clientcmd.Load(rc.Data)
	require.NoError(t, err)
	assert.Len(t, cfg.Contexts, 1)
	require.Contains(t, cfg.AuthInfos, "plugin-user")
	assert.Nil(t, cfg.AuthInfos["plugin-user"].Exec)
	assert.NotContains(t, string(rc.Data), "gke-auth")
	assert.NotContains(t, string(rc.Data), "kubeauth")
	// Files are flattened into the data.
	assert.Equal(t, []byte("CA-DATA"), cfg.Clusters["cl"].CertificateAuthorityData)
	assert.Empty(t, cfg.Clusters["cl"].CertificateAuthority)
}

func TestCreateRootKubeConfig_staticCredentialsAreUntouched(t *testing.T) {
	rc, err := CreateRootKubeConfig(t.Context(), loadTestConfig(t), "static")
	require.NoError(t, err)
	assert.Nil(t, rc.Exec)
	assert.Empty(t, rc.TokenFile)

	cfg, err := clientcmd.Load(rc.Data)
	require.NoError(t, err)
	assert.Equal(t, "static", cfg.CurrentContext)
	require.Contains(t, cfg.AuthInfos, "static-user")
	assert.Equal(t, "static-token", cfg.AuthInfos["static-user"].Token)
	assert.NotContains(t, cfg.AuthInfos, "plugin-user")
}

func TestCreateRootKubeConfig_mergesOverrides(t *testing.T) {
	path := loadTestPath(t)
	dir := t.TempDir()
	overrideCA := filepath.Join(dir, "override-ca.crt")
	require.NoError(t, os.WriteFile(overrideCA, []byte("OVERRIDE-CA"), 0o600))

	loader := loadOverridingConfig(t, path, &clientcmd.ConfigOverrides{
		CurrentContext: "static",
		AuthInfo: clientcmdapi.AuthInfo{
			Token:             "override-token",
			Impersonate:       "someone",
			ImpersonateGroups: []string{"g1", "g2"},
		},
		ClusterInfo: clientcmdapi.Cluster{
			Server:               "https://override.example.com:6443",
			CertificateAuthority: overrideCA,
			TLSServerName:        "tls.example.com",
		},
		Context: clientcmdapi.Context{Namespace: "other"},
	})
	rc, err := CreateRootKubeConfig(t.Context(), loader, "")
	require.NoError(t, err)
	assert.Nil(t, rc.Exec)

	cfg, err := clientcmd.Load(rc.Data)
	require.NoError(t, err)
	assert.Equal(t, "static", cfg.CurrentContext)
	assert.Equal(t, "other", cfg.Contexts["static"].Namespace)

	cl := cfg.Clusters["cl"]
	require.NotNil(t, cl)
	assert.Equal(t, "https://override.example.com:6443", cl.Server)
	assert.Equal(t, "tls.example.com", cl.TLSServerName)
	assert.Equal(t, []byte("OVERRIDE-CA"), cl.CertificateAuthorityData)
	assert.Empty(t, cl.CertificateAuthority)

	ai := cfg.AuthInfos["static-user"]
	require.NotNil(t, ai)
	assert.Equal(t, "override-token", ai.Token)
	assert.Equal(t, "someone", ai.Impersonate)
	assert.Equal(t, []string{"g1", "g2"}, ai.ImpersonateGroups)
}

func TestCreateRootKubeConfig_pluginOfMergedUser(t *testing.T) {
	// --user selects the plugin user for a context that has the static user.
	loader := loadOverridingConfig(t, loadTestPath(t), &clientcmd.ConfigOverrides{
		CurrentContext: "static",
		Context:        clientcmdapi.Context{AuthInfo: "plugin-user"},
	})
	rc, err := CreateRootKubeConfig(t.Context(), loader, "")
	require.NoError(t, err)
	require.NotNil(t, rc.Exec)
	assert.Equal(t, "gke-auth", rc.Exec.Command)

	cfg, err := clientcmd.Load(rc.Data)
	require.NoError(t, err)
	assert.Equal(t, "plugin-user", cfg.Contexts["static"].AuthInfo)
	assert.Nil(t, cfg.AuthInfos["plugin-user"].Exec)
}

func TestCreateRootKubeConfig_noFileReferences(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
		return p
	}
	ca := write("ca.crt", "CA-DATA")
	cert := write("client.crt", "CERT-DATA")
	key := write("client.key", "KEY-DATA")
	path := write("config", fmt.Sprintf(`
apiVersion: v1
kind: Config
current-context: c
contexts:
- name: c
  context: {cluster: cl, user: u}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:6443", certificate-authority: %s}
users:
- name: u
  user: {client-certificate: %s, client-key: %s}
`, ca, cert, key))
	rc, err := CreateRootKubeConfig(t.Context(), loadOverridingConfig(t, path, &clientcmd.ConfigOverrides{}), "")
	require.NoError(t, err)

	cfg, err := clientcmd.Load(rc.Data)
	require.NoError(t, err)
	assert.Equal(t, []byte("CA-DATA"), cfg.Clusters["cl"].CertificateAuthorityData)
	assert.Equal(t, []byte("CERT-DATA"), cfg.AuthInfos["u"].ClientCertificateData)
	assert.Equal(t, []byte("KEY-DATA"), cfg.AuthInfos["u"].ClientKeyData)
	for _, ai := range cfg.AuthInfos {
		assert.Empty(t, ai.ClientCertificate)
		assert.Empty(t, ai.ClientKey)
		assert.Empty(t, ai.TokenFile)
		assert.Nil(t, ai.Exec)
		assert.Nil(t, ai.AuthProvider)
	}
	for _, cl := range cfg.Clusters {
		assert.Empty(t, cl.CertificateAuthority)
	}
}

func TestCreateRootKubeConfig_tokenFileIsReturnedSeparately(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("file-token\n"), 0o600))
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `
apiVersion: v1
kind: Config
current-context: c
contexts:
- name: c
  context: {cluster: cl, user: u}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:6443"}
users:
- name: u
  user: {tokenFile: token}
`), 0o600))
	rc, err := CreateRootKubeConfig(t.Context(), loadOverridingConfig(t, path, &clientcmd.ConfigOverrides{}), "")
	require.NoError(t, err)
	// A relative token file is relative to the kubeconfig file.
	assert.Equal(t, tokenFile, rc.TokenFile)
	assert.NotContains(t, string(rc.Data), "token")
}

func TestCreateRootKubeConfig_tokenOverrideForContextWithoutUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(`
apiVersion: v1
kind: Config
current-context: c
contexts:
- name: c
  context: {cluster: cl}
clusters:
- name: cl
  cluster: {server: "https://127.0.0.1:6443"}
`), 0o600))
	loader := loadOverridingConfig(t, path, &clientcmd.ConfigOverrides{AuthInfo: clientcmdapi.AuthInfo{Token: "flag-token"}})
	rc, err := CreateRootKubeConfig(t.Context(), loader, "")
	require.NoError(t, err)

	rest, err := clientcmd.RESTConfigFromKubeConfig(rc.Data)
	require.NoError(t, err)
	assert.Equal(t, "flag-token", rest.BearerToken)
}
