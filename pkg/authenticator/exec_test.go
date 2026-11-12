package authenticator

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const execInfoTestEnv = "TELEPRESENCE_TEST_EXEC_INFO"

func TestExecInfoHelper(t *testing.T) {
	if os.Getenv(execInfoTestEnv) != "1" {
		return
	}
	fmt.Println(os.Getenv("KUBERNETES_EXEC_INFO"))
	os.Exit(0)
}

func execInfoConfig(apiVersion string) *clientcmdapi.ExecConfig {
	return &clientcmdapi.ExecConfig{
		APIVersion:      apiVersion,
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		Command:         os.Args[0],
		Args:            []string{"-test.run=^TestExecInfoHelper$"},
		Env: []clientcmdapi.ExecEnvVar{
			{Name: execInfoTestEnv, Value: "1"},
			{Name: "KUBERNETES_EXEC_INFO", Value: "stale-configured-value"},
		},
	}
}

func TestExecCredentialsAPIVersion(t *testing.T) {
	t.Setenv("KUBERNETES_EXEC_INFO", "stale-inherited-value")
	for _, apiVersion := range []string{"client.authentication.k8s.io/v1", "client.authentication.k8s.io/v1beta1"} {
		t.Run(apiVersion, func(t *testing.T) {
			result, err := ResolveExecConfig(t.Context(), execInfoConfig(apiVersion))
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf(`{"apiVersion":%q,"kind":"ExecCredential","spec":{"interactive":false}}`, apiVersion), string(result))
		})
	}
}

type execInfoClientConfigProvider struct {
	config clientcmd.ClientConfig
}

func (p execInfoClientConfigProvider) ClientConfig() (clientcmd.ClientConfig, error) {
	return p.config, nil
}

func TestCredentialServiceAPIVersion(t *testing.T) {
	for _, apiVersion := range []string{"client.authentication.k8s.io/v1", "client.authentication.k8s.io/v1beta1"} {
		t.Run(apiVersion, func(t *testing.T) {
			config := clientcmdapi.NewConfig()
			config.Contexts["test"] = &clientcmdapi.Context{AuthInfo: "user"}
			config.AuthInfos["user"] = &clientcmdapi.AuthInfo{Exec: execInfoConfig(apiVersion)}
			loader := clientcmd.NewNonInteractiveClientConfig(*config, "test", &clientcmd.ConfigOverrides{}, nil)
			service := NewService(execInfoClientConfigProvider{config: loader})
			result, err := service.GetExecCredentials(t.Context(), "test")
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf(`{"apiVersion":%q,"kind":"ExecCredential","spec":{"interactive":false}}`, apiVersion), string(result))
		})
	}
}

func TestExecCredentialsNoLocalEnv(t *testing.T) {
	t.Setenv("GLOBAL_ENV", "global-val")

	config := &clientcmdapi.ExecConfig{
		Command: "sh",
		Args:    []string{"-c", "echo $GLOBAL_ENV/$LOCAL_ENV"},
	}
	result, err := ResolveExecConfig(context.Background(), config)
	assert.NoError(t, err)
	assert.Equal(t, string(result), "global-val/\n")
}

func TestExecCredentialsYesLocalEnv(t *testing.T) {
	t.Setenv("GLOBAL_ENV", "global-val")

	config := &clientcmdapi.ExecConfig{
		Command: "sh",
		Args:    []string{"-c", "echo $GLOBAL_ENV/$LOCAL_ENV"},
		Env:     []clientcmdapi.ExecEnvVar{{Name: "LOCAL_ENV", Value: "local-val"}},
	}
	result, err := ResolveExecConfig(context.Background(), config)
	assert.NoError(t, err)
	assert.Equal(t, string(result), "global-val/local-val\n")
}
