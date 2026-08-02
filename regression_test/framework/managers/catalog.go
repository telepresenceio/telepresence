package managers

import "strings"

// Default is the manager configuration used by suites with no special
// requirements: Baseline, with no overlay values.
//
//nolint:gochecknoglobals // catalog entry
var Default = Spec{Key: "default"}

// InjectorDisabled returns a manager spec with the agent-injector webhook
// (agentInjector.enabled) turned off entirely: no workload in any managed
// namespace can be intercepted, since no agent can be injected.
func InjectorDisabled() Spec {
	disabled := false
	return Spec{
		Key:    "injector-disabled",
		Values: Values{AgentInjector: AgentInjector{Enabled: &disabled}},
	}
}

// InjectPolicy returns a manager spec overriding agentInjector.injectPolicy
// to p (one of the values.schema.yaml enum: "OnDemand" or "WhenEnabled").
// The Key is parameterized so specs for different policies hash (and hence
// helm-release) differently.
func InjectPolicy(p string) Spec {
	return Spec{
		Key:    "inject-policy/" + p,
		Values: Values{AgentInjector: AgentInjector{InjectPolicy: p}},
	}
}

// CertRegen returns a manager spec that forces the mutating webhook's
// certificate to be regenerated (agentInjector.certificate.regenerate=true)
// using accessMethod ("watch" or "mount") to read it
// (agentInjector.certificate.accessMethod). Mirrors the --set combination
// integration_test/injector_test.go uses.
func CertRegen(accessMethod string) Spec {
	return Spec{
		Key: "cert-regen/" + accessMethod,
		Values: Values{AgentInjector: AgentInjector{
			Certificate: Certificate{AccessMethod: accessMethod, Regenerate: true},
		}},
	}
}

// StaticNamespaces returns a manager spec that manages exactly ns via a
// static namespaces list instead of the default namespaceSelector; Merge
// nulls NamespaceSelector when Namespaces is set (see values.go), so this
// spec fully replaces Baseline's selector rather than adding to it.
func StaticNamespaces(ns ...string) Spec {
	return Spec{
		Key:    "static-namespaces/" + strings.Join(ns, ","),
		Values: Values{Namespaces: ns},
	}
}

// NodeAgent returns a manager spec with node-hosted traffic-agent mode
// enabled (nodeAgent.enabled=true) and the sidecar's H2C probing turned off
// (agent.enableH2cProbing=false: a node-agent Job enters an existing pod's
// namespaces and has no sidecar of its own to probe, so the probe is
// disabled the same way the superseded suite disabled it for every
// sidecar-carrying workload it shared a manager with). Mirrors
// node_agent_test.go's nodeAgentSuite install.
func NodeAgent() Spec {
	disableH2c := false
	return Spec{
		Key: "node-agent",
		Values: Values{
			NodeAgent: NodeAgentValues{Enabled: true},
			Agent:     AgentValues{EnableH2cProbing: &disableH2c},
		},
	}
}

// NodeAgentNoInjector returns a manager spec with the agent-injector webhook
// disabled entirely (agentInjector.enabled=false) and node-hosted
// traffic-agent mode enabled (nodeAgent.enabled=true): no sidecar can ever
// be injected, but node-agent attaches (intercept/ingest) still work.
// Mirrors node_agent_no_injector_test.go's nodeAgentNoInjectorSuite install.
func NodeAgentNoInjector() Spec {
	disabled := false
	return Spec{
		Key: "node-agent-no-injector",
		Values: Values{
			AgentInjector: AgentInjector{Enabled: &disabled},
			NodeAgent:     NodeAgentValues{Enabled: true},
		},
	}
}

// NodeAgentClientDefault returns a manager spec with node-hosted
// traffic-agent mode enabled cluster-wide AND served to connecting clients
// as their --node-agent default (client.nodeAgent.enabled=true): a flagless
// intercept/ingest/wiretap uses node-agent mode without --node-agent, while
// --node-agent=false still overrides it back to a sidecar. H2C probing is
// disabled for the same reason as NodeAgent(). Mirrors
// node_agent_cluster_default_test.go's nodeAgentClusterDefaultSuite install.
func NodeAgentClientDefault() Spec {
	disableH2c := false
	return Spec{
		Key: "node-agent-client-default",
		Values: Values{
			NodeAgent: NodeAgentValues{Enabled: true},
			Client:    Client{NodeAgent: ClientNodeAgent{Enabled: true}},
			Agent:     AgentValues{EnableH2cProbing: &disableH2c},
		},
	}
}

// QuicNodePortPort is the fixed NodePort quic_test.go pinned its QUIC
// tunnel Service to (quicTunnelSuite's quicNodePort). It has to be a known,
// static value rather than a Kubernetes-assigned one: the quicTunnel.
// service.nodePort Helm value must be set before the Service exists.
const QuicNodePortPort = 30777

// QuicNodePort returns a manager spec with the QUIC tunnel enabled behind a
// NodePort Service pinned to QuicNodePortPort, and node-hosted
// traffic-agent mode enabled (quic_test.go also exercises a node-agent
// attach over the transport). quicTunnel.externalHost/externalPort are left
// unset, so the traffic-manager self-discovers the advertised endpoint from
// the cluster's Node addresses and the Service's assigned port -- the
// zero-configuration path quic_test.go's Test_ZZDiscoveryNodePort proves
// works, and the only externally-reachable-host strategy that doesn't
// require a live cluster lookup at spec-construction time (quic_test.go's
// own SetupSuite instead discovered a Node's InternalIP with kubectl and
// set externalHost explicitly; a suite that needs that -- e.g. to force a
// specific unreachable endpoint for the fallback scenario -- layers its own
// quicTunnel.externalHost/externalPort overlay with Merge on top of this
// spec's Values rather than going through a separate catalog entry).
func QuicNodePort() Spec {
	return Spec{
		Key: "quic-nodeport",
		Values: Values{
			NodeAgent: NodeAgentValues{Enabled: true},
			QuicTunnel: QuicTunnel{
				Enabled: true,
				Service: QuicTunnelService{Type: "NodePort", NodePort: QuicNodePortPort},
			},
		},
	}
}

// QuicRelay returns the same manager spec as QuicNodePort. In
// quic_test.go, the relay scenario (Test_AgentPortForwardDisabledRelaysOverQuic)
// installs no manager-side configuration beyond the suite's regular
// NodePort QUIC release; it forces client traffic to relay through the
// manager entirely via the connecting client's own local
// cluster.agentPortForward=false setting (pkg/client/config.go's
// Cluster.AgentPortForward), which has no chart equivalent -- client.* only
// seeds the config a connecting client starts from, not this kind of
// per-session override. QuicRelay exists so a suite can name the manager
// spec the relay scenario depends on without reaching for QuicNodePort
// directly; reusing its Key (rather than minting a new one) also means
// Mutate-ing between the two scenarios costs no extra helm upgrade, mirroring
// quic_test.go's single shared install.
func QuicRelay() Spec {
	return QuicNodePort()
}

// AuthPermissive returns a manager spec that explicitly sets
// security.authentication.mode to "permissive" -- the chart's own default,
// set here so the auth area's permissive scenario has a spec named for what
// it tests, even though the resulting release is behaviorally identical to
// Default's.
func AuthPermissive() Spec {
	return Spec{
		Key:    "auth-permissive",
		Values: Values{Security: Security{Authentication: Authentication{Mode: "permissive"}}},
	}
}

// AuthEnforcing returns a manager spec with
// security.authentication.mode=enforcing: only a caller whose identity is
// both authenticated and authorized may use the traffic-manager. Mirrors
// manager_auth_test.go's managerAuthSuite install. A suite exercising the
// x509-disabled variant (Test_EnforcingRejectsCertOnlyClientWhenX509Disabled)
// layers Security{Authentication{X509: X509{Enabled: &falseVal}}} on top
// with Merge rather than going through a separate catalog entry.
func AuthEnforcing() Spec {
	return Spec{
		Key:    "auth-enforcing",
		Values: Values{Security: Security{Authentication: Authentication{Mode: "enforcing"}}},
	}
}

// UsageTo returns a manager spec with usage reporting enabled and pointed
// at addr (a host:port), dialed without TLS: the local usage collector
// (rt.NewUsageCollector) or any other plain-text gRPC usg-service listener.
// Mirrors usage_reporting_test.go's usageReportingSuite install
// (usage.enabled=true, usage.collectorAddress=addr, usage.insecure=true).
func UsageTo(addr string) Spec {
	return Spec{
		Key: "usage-to/" + addr,
		Values: Values{Usage: Usage{
			Enabled:          true,
			CollectorAddress: addr,
			Insecure:         true,
		}},
	}
}

// Compat returns a manager spec with compatibility.version set to version:
// for testing only, it makes the manager return Unimplemented for every RPC
// introduced after that version (see checkCompat in
// cmd/traffic/cmd/manager/service.go), exercising the client's fallback
// paths against a manager that never shipped the newer RPC surface.
func Compat(version string) Spec {
	return Spec{
		Key:    "compat/" + version,
		Values: Values{Compatibility: Compatibility{Version: version}},
	}
}

// ClientConfig returns a manager spec identified by key, applying v as the
// full overlay: a generic constructor for cluster-served client.* config
// combinations (dns.includeSuffixes, routing.*, nodeAgent.enabled,
// logLevels.*) that don't warrant their own named catalog entry. key
// distinguishes the resulting release from every other spec (including
// other ClientConfig calls); callers building a client-config-only overlay
// typically pass Values{Client: Client{...}}, but v is not restricted to
// the Client field, so this doubles as the general "or equivalent
// parameterized spec" escape hatch for combinations the catalog doesn't
// name.
func ClientConfig(key string, v Values) Spec {
	return Spec{
		Key:    "client-config/" + key,
		Values: v,
	}
}
