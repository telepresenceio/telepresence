package quicfwd_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

// TestManagerSNI_MatchesQuictunnelServerName guards against the two constants drifting
// apart: quicfwd.ManagerSNI is a duplicate, not an import, of
// quictunnel.ServerName (see the comment on ManagerSNI for why), so nothing but a test
// keeps them in sync.
func TestManagerSNI_MatchesQuictunnelServerName(t *testing.T) {
	assert.Equal(t, quictunnel.ServerName, quicfwd.ManagerSNI)
}

func TestAgentSNI_ParseSNI_RoundTrip(t *testing.T) {
	podUID := "3f9b6f2e-1234-4abc-9def-0123456789ab"
	sni := quicfwd.AgentSNI(podUID)
	assert.Equal(t, "3f9b6f2e-1234-4abc-9def-0123456789ab.agent.telepresence", sni)

	kind, uid := quicfwd.ParseSNI(sni)
	assert.Equal(t, quicfwd.BackendAgent, kind)
	assert.Equal(t, podUID, uid)
}

func TestParseSNI_Manager(t *testing.T) {
	kind, uid := quicfwd.ParseSNI(quicfwd.ManagerSNI)
	assert.Equal(t, quicfwd.BackendManager, kind)
	assert.Empty(t, uid)
}

func TestParseSNI_Unknown(t *testing.T) {
	tests := []string{
		"",
		"evil.example.com",
		"traffic-manager.telepresence.evil.com",
		".agent.telepresence", // empty pod UID
		"agent.telepresence",  // missing the leading dot-separated UID
	}
	for _, sni := range tests {
		kind, uid := quicfwd.ParseSNI(sni)
		assert.Equal(t, quicfwd.BackendUnknown, kind, "sni=%q", sni)
		assert.Empty(t, uid, "sni=%q", sni)
	}
}
