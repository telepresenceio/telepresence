package patcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
