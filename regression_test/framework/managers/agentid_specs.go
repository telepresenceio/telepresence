package managers

// AgentidPreAccessCallVersion is the newest traffic-manager version that
// does not implement AuthorizeSessionAccess.
const AgentidPreAccessCallVersion = "2.33.4"

// AgentidEnforcingPreAccessCall returns a manager spec combining
// AuthEnforcing's security.authentication.mode=enforcing with
// compatibility.agentVersion=AgentidPreAccessCallVersion: the traffic-manager
// enforces authentication but answers Unimplemented for
// AuthorizeSessionAccess. Every other RPC that enforcing mode relies on
// was introduced at or before that version, so it stays available.
func AgentidEnforcingPreAccessCall() Spec {
	return Spec{
		Key:    "agentid-auth-enforcing-compat/" + AgentidPreAccessCallVersion,
		Values: Merge(AuthEnforcing().Values, CompatAgents(AgentidPreAccessCallVersion).Values),
	}
}

// AgentidPreQuicAgentCertVersion is the newest traffic-manager version that
// does not implement GetQuicAgentCert.
const AgentidPreQuicAgentCertVersion = "2.30.1"

// AgentidEnforcingPreQuicAgentCert returns a manager spec combining
// AuthEnforcing's security.authentication.mode=enforcing with
// compatibility.agentVersion=AgentidPreQuicAgentCertVersion: the traffic-manager
// answers Unimplemented for GetQuicAgentCert, so an agent never receives
// session credential material.
func AgentidEnforcingPreQuicAgentCert() Spec {
	return Spec{
		Key:    "agentid-auth-enforcing-compat/" + AgentidPreQuicAgentCertVersion,
		Values: Merge(AuthEnforcing().Values, CompatAgents(AgentidPreQuicAgentCertVersion).Values),
	}
}
