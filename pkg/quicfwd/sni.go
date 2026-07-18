package quicfwd

import "strings"

// ManagerSNI is the SNI the Telepresence client dials when connecting to the
// traffic-manager's QUIC endpoint through the forwarder. It must equal
// quictunnel.ServerName (cmd/traffic/cmd/manager/quictunnel/ca.go) -- that package owns
// the manager's server certificate, which is minted for exactly that DNS name, and the
// two must never drift apart. It is duplicated here, rather than imported, because this
// package must not depend on anything under cmd/; a test in this package asserts the
// two constants stay equal.
const ManagerSNI = "traffic-manager.telepresence"

// agentSNISuffix is appended to a pod UID to form the SNI an agent's QUIC listener
// presents. Pod UID rather than pod name or namespace/name because it is already the
// stable identity `AgentPodInfo` and the rest of the agent-tracking code use, is
// guaranteed unique across the cluster's lifetime (unlike a name, which can be reused
// after a pod is deleted and recreated), and contains no characters that need escaping
// in a DNS name.
const agentSNISuffix = ".agent.telepresence"

// Backend identifies which kind of QUIC listener an SNI name resolves to.
type Backend int

const (
	// BackendUnknown means the SNI didn't match any known naming scheme. The
	// forwarder's response to this, per "The forwarder" in
	// docs/reference/quic-transport-architecture.md, is to drop the packet silently: an SNI
	// is only ever trusted when it names a real backend.
	BackendUnknown Backend = iota
	BackendManager
	BackendAgent
)

func (b Backend) String() string {
	switch b {
	case BackendManager:
		return "manager"
	case BackendAgent:
		return "agent"
	default:
		return "unknown"
	}
}

// AgentSNI returns the SNI a traffic-agent with the given pod UID presents on its QUIC
// listener, and that a client or the forwarder uses to address it.
func AgentSNI(podUID string) string {
	return podUID + agentSNISuffix
}

// ParseSNI is the inverse of ManagerSNI/AgentSNI: given an SNI observed in a
// ClientHello, it reports which kind of backend it names and, for an agent, the pod
// UID. It returns (BackendUnknown, "") for anything that doesn't match either scheme --
// including an agent suffix with an empty UID, which cannot be a genuine AgentSNI
// output and must be rejected the same as any other unrecognized name.
func ParseSNI(sni string) (kind Backend, podUID string) {
	if sni == ManagerSNI {
		return BackendManager, ""
	}
	if uid, ok := strings.CutSuffix(sni, agentSNISuffix); ok && uid != "" {
		return BackendAgent, uid
	}
	return BackendUnknown, ""
}
