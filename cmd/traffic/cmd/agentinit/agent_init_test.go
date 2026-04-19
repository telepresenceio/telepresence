package agentinit

import (
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func TestTrafficAgentUIDDefaultsToProcessUID(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(os.Getuid()), uid)
}

func TestTrafficAgentUIDUsesEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, "1000", uid)
}

func TestTrafficAgentUIDRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "not-a-uid")

	_, err := trafficAgentUID()
	require.Error(t, err)
}
