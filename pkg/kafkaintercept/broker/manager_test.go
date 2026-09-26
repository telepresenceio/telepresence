package broker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForeignMember(t *testing.T) {
	member, ok := foreignMember([]string{"tp-drain-alice"}, "tp-drain-alice")
	require.False(t, ok)
	require.Empty(t, member)

	member, ok = foreignMember([]string{"tp-drain-alice", "local-consumer-1"}, "tp-drain-alice")
	require.True(t, ok)
	require.Equal(t, "local-consumer-1", member)
}
