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

func loadTestConfig(t *testing.T) clientcmd.ClientConfig {
	t.Helper()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	require.NoError(t, os.WriteFile(ca, []byte("CA-DATA"), 0o600))
	path := filepath.Join(dir, "config")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, withoutExecKubeconfig, ca), 0o600))
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, &clientcmd.ConfigOverrides{})
}

func TestCreateKubeConfigWithoutExec_removesExec(t *testing.T) {
	data, ec, err := CreateKubeConfigWithoutExec(t.Context(), loadTestConfig(t), "")
	require.NoError(t, err)

	require.NotNil(t, ec)
	assert.Equal(t, "gke-auth", ec.Command)
	assert.Equal(t, []string{"--project", "p"}, ec.Args)
	assert.Equal(t, "client.authentication.k8s.io/v1", ec.APIVersion)

	cfg, err := clientcmd.Load(data)
	require.NoError(t, err)
	assert.Len(t, cfg.Contexts, 1)
	require.Contains(t, cfg.AuthInfos, "plugin-user")
	assert.Nil(t, cfg.AuthInfos["plugin-user"].Exec)
	assert.NotContains(t, string(data), "gke-auth")
	assert.NotContains(t, string(data), "kubeauth")
	// Files are flattened into the data.
	assert.Equal(t, []byte("CA-DATA"), cfg.Clusters["cl"].CertificateAuthorityData)
	assert.Empty(t, cfg.Clusters["cl"].CertificateAuthority)
}

func TestCreateKubeConfigWithoutExec_staticCredentialsAreUntouched(t *testing.T) {
	data, ec, err := CreateKubeConfigWithoutExec(t.Context(), loadTestConfig(t), "static")
	require.NoError(t, err)
	assert.Nil(t, ec)

	cfg, err := clientcmd.Load(data)
	require.NoError(t, err)
	assert.Equal(t, "static", cfg.CurrentContext)
	require.Contains(t, cfg.AuthInfos, "static-user")
	assert.Equal(t, "static-token", cfg.AuthInfos["static-user"].Token)
	assert.NotContains(t, cfg.AuthInfos, "plugin-user")
}
