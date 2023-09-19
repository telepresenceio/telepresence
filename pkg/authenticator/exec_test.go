package authenticator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestExecCredentialsNoLocalEnv(t *testing.T) {
	t.Setenv("GLOBAL_ENV", "global-val")

	config := &clientcmdapi.ExecConfig{
		Command: "sh",
		Args:    []string{"-c", "echo $GLOBAL_ENV/$LOCAL_ENV"},
	}
	result, err := execCredentialBinary{}.Resolve(context.Background(), config)
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
	result, err := execCredentialBinary{}.Resolve(context.Background(), config)
	assert.NoError(t, err)
	assert.Equal(t, string(result), "global-val/local-val\n")
}
