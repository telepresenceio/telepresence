package setup

import (
	"fmt"
	"os"
	"slices"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// LoadInputValues reads a Helm values document (typically one produced by
// --output) whose settings become pinned defaults.
func LoadInputValues(path string) (*helm.Values, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errcat.User.Errorf(err, "reading input values %q", path)
	}
	values, err := helm.ParseValues(data)
	if err != nil {
		return nil, errcat.User.Errorf(err, "parsing input values %q", path)
	}
	return values, nil
}

// PinAnswers fills in the answers and presets that in determines
// unambiguously, so the interview skips the pinned questions. A field in
// leaves unset is left unpinned. An answer already marked preset in pre is
// left untouched.
func PinAnswers(in *helm.Values, a *Answers, pre *Preset) {
	pinAttachReplace(in, a, pre)
	pinQuic(in, a)
	pinManagedScope(in, a, pre)
	pinMappedNamespaces(in, a, pre)
	pinAllowConflicts(in, a, pre)
	pinSecurity(in, a, pre)
	pinExternalEndpoint(in, a, pre)
	pinLegacyAccess(in, a, pre)
}

// pinAttachReplace derives the attach/replace pins from agentInjector.enabled
// and nodeAgent.enabled: either alone pins attach (and, for the injector
// alone, replace false too); both together also pin replace.
func pinAttachReplace(in *helm.Values, a *Answers, pre *Preset) {
	injector, nodeAgent := in.AgentInjector.Enabled, in.NodeAgent.Enabled
	switch {
	case injector != nil && nodeAgent != nil:
		attach := *injector || *nodeAgent
		if !pre.Attach {
			a.Attach = attach
			pre.Attach = true
		}
		if attach && !pre.Replace {
			a.Replace = *nodeAgent && *injector
			pre.Replace = true
		}
	case nodeAgent != nil && *nodeAgent:
		if !pre.Attach {
			a.Attach = true
			pre.Attach = true
		}
	case injector != nil && *injector:
		if !pre.Attach {
			a.Attach = true
			pre.Attach = true
		}
		if !pre.Replace {
			a.Replace = false
			pre.Replace = true
		}
	}
}

// pinQuic pins the QUIC Tri override from quicTunnel.enabled, while the
// answer is still at its auto default.
func pinQuic(in *helm.Values, a *Answers) {
	quic := in.QuicTunnel.Enabled
	if quic == nil || (a.Quic != "" && a.Quic != TriAuto) {
		return
	}
	if *quic {
		a.Quic = TriOn
	} else {
		a.Quic = TriOff
	}
}

// pinManagedScope pins the managed scope and its namespace list or label
// selector, matched in order: namespaces then namespaceSelector.
func pinManagedScope(in *helm.Values, a *Answers, pre *Preset) {
	if pre.ManagedScope {
		return
	}
	switch {
	case len(in.Namespaces) > 0:
		a.ManagedScope = ManagedScopeNamespaces
		pre.ManagedScope = true
		if !pre.ManagedNamespaces {
			a.ManagedNamespaces = slices.Clone(in.Namespaces)
			pre.ManagedNamespaces = true
		}
	case len(in.NamespaceSelector.MatchLabels) > 0:
		a.ManagedScope = ManagedScopeSelector
		pre.ManagedScope = true
		if len(a.SelectorLabels) == 0 {
			a.SelectorLabels = in.NamespaceSelector.MatchLabels
		}
	}
}

// pinMappedNamespaces pins the client mapped-namespaces default from
// client.cluster.mappedNamespaces; it is independent of the managed scope.
func pinMappedNamespaces(in *helm.Values, a *Answers, pre *Preset) {
	if mapped := in.Client.Cluster.MappedNamespaces; len(mapped) > 0 && !pre.MappedNamespaces {
		a.MappedNamespaces = slices.Clone(mapped)
		pre.MappedNamespaces = true
	}
}

// pinAllowConflicts pins the conflicts question from
// client.routing.allowConflictingSubnets: the input's list is authoritative,
// so answering yes lets the engine emit its own list and the reconcile pass
// guard the pinned value.
func pinAllowConflicts(in *helm.Values, a *Answers, pre *Preset) {
	if subnets := in.Client.Routing.AllowConflictingSubnets; len(subnets) > 0 && !pre.AllowConflicts {
		a.AllowConflicts = true
		pre.AllowConflicts = true
	}
}

// pinSecurity pins enforce-auth and the required grant from
// security.authentication.mode and security.authorization.requiredGrant.
func pinSecurity(in *helm.Values, a *Answers, pre *Preset) {
	if mode := in.Security.Authentication.Mode; mode != nil && !pre.EnforceAuth {
		a.EnforceAuth = *mode == helm.AuthModeEnforcing
		pre.EnforceAuth = true
	}
	if grant := in.Security.Authorization.RequiredGrant; grant != nil && !pre.RequiredGrant {
		if _, err := ParseRequiredGrant(*grant); err == nil {
			a.RequiredGrant = *grant
			pre.RequiredGrant = true
		}
	}
}

// pinExternalEndpoint pins whether Direct Connect is enabled from
// externalEndpoint.enabled, and, when enabled, the certificate source it
// names.
func pinExternalEndpoint(in *helm.Values, a *Answers, pre *Preset) {
	enabled := in.ExternalEndpoint.Enabled
	if enabled == nil || pre.ExternalEndpoint {
		return
	}
	a.ExternalEndpoint = *enabled
	pre.ExternalEndpoint = true
	if !*enabled {
		return
	}
	a.ExternalTLSSecret = deref(in.ExternalEndpoint.TLS.SecretName)
	if !deref(in.ExternalEndpoint.TLS.CertManager.Enabled) {
		return
	}
	cm := in.ExternalEndpoint.TLS.CertManager
	a.ExternalCertManager = &CertManagerAnswer{
		IssuerName: deref(cm.IssuerRef.Name),
		IssuerKind: deref(cm.IssuerRef.Kind),
		DNSNames:   cm.DNSNames,
	}
}

// pinLegacyAccess pins the legacy-access answer from clientRbac.legacyAccess.
func pinLegacyAccess(in *helm.Values, a *Answers, pre *Preset) {
	if legacy := in.ClientRbac.LegacyAccess; legacy != nil && !pre.LegacyAccess {
		a.LegacyAccess = *legacy
		pre.LegacyAccess = true
	}
}

// ConsultFunc resolves a conflict between an input-pinned value and the
// engine's recommendation; returning true keeps the input value.
type ConsultFunc func(path, inputVal, recVal string) (bool, error)

// ConsultInput is the interactive ConsultFunc: it asks whether to keep the
// conflicting input value, defaulting to keep.
func (iv *Interviewer) ConsultInput(path, inputVal, recVal string) (bool, error) {
	return iv.askYesNo(fmt.Sprintf("The input sets %s=%s; probing recommends %s. Keep the input value? ", path, inputVal, recVal), true)
}

// ValidateValues checks the final values, wherever they came from, against
// the hard incompatibilities the probes uncovered. Only fields set in the
// values participate. The missing-install-privilege check is a hard error
// only when applying: RecommendWithInput already downgraded it to a warning
// note for validation-mode runs, so re-raising it here would defeat that.
// Every other incompatibility (webhook, replicaCount) stays an error
// regardless of mode.
func (f *ClusterFacts) ValidateValues(vals *helm.Values, applying bool) error {
	if p := vals.AgentInjector.Enabled; p != nil && *p {
		if f.Webhook.CanCreate.Verdict == VerdictNo {
			return webhookDeniedError()
		}
	}
	hasNamespaces := len(vals.Namespaces) > 0
	hasSelector := len(vals.NamespaceSelector.MatchLabels) > 0
	if applying && !hasNamespaces && !hasSelector && f.Privileges.ClusterWide.Verdict == VerdictNo {
		return f.clusterWideDeniedError()
	}
	if deref(vals.QuicTunnel.Enabled) {
		if rc := vals.ReplicaCount; rc != nil && *rc > 1 {
			return errcat.User.Newf("quicTunnel.enabled requires replicaCount 1, but the values set replicaCount %d", *rc)
		}
	}
	if deref(vals.ExternalEndpoint.Enabled) {
		if !vals.AuthEnforced() {
			return errcat.User.New("externalEndpoint.enabled requires security.authentication.mode: enforcing")
		}
		secretSet := deref(vals.ExternalEndpoint.TLS.SecretName) != ""
		cmEnabled := deref(vals.ExternalEndpoint.TLS.CertManager.Enabled)
		if secretSet == cmEnabled {
			return errcat.User.New("externalEndpoint.tls: set exactly one of tls.secretName or tls.certManager.enabled")
		}
	}
	return nil
}
