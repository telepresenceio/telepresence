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
