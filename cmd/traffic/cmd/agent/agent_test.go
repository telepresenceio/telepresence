package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/sethvargo/go-envconfig"
	"github.com/stretchr/testify/require"
)

func TestConfig(t *testing.T) {
	envs := appEnv{
		"ENV_A": "value a",
		"ENV_B": "value b",
	}
	js, err := json.Marshal(envs)
	require.NoError(t, err)

	os.Setenv("AGENT_NAME", "some-agent")
	os.Setenv("APP_PORT", "9000")
	os.Setenv("APP_ENVIRONMENT", string(js))
	config := Config{}
	require.NoError(t, envconfig.Process(context.Background(), &config))
	require.Equal(t, envs, config.AppEnvironment)
}
