package setup

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/blang/semver/v4"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Action is what the proposal will do to the traffic-manager installation.
type Action string

const (
	ActionInstall Action = "install"
	ActionUpgrade Action = "upgrade"
	ActionNone    Action = "none"
)

// NoteLevel classifies a proposal note.
type NoteLevel string

const (
	NoteInfo    NoteLevel = "info"
	NoteWarning NoteLevel = "warning"
)

// Note is an explanation or caveat attached to a proposal.
type Note struct {
	Level NoteLevel `json:"level"`
	Text  string    `json:"text"`
}

// notes collects the info and warning notes a recommendation accumulates.
type notes struct {
	list []Note
}

func (n *notes) info(text string) { n.list = append(n.list, Note{Level: NoteInfo, Text: text}) }

func (n *notes) warn(text string) { n.list = append(n.list, Note{Level: NoteWarning, Text: text}) }

// Proposal is the outcome of the decision engine: the values to install or
// upgrade with, the planned action, and the notes explaining both.
type Proposal struct {
	Action      Action       `json:"action"`
	Values      *helm.Values `json:"values"`                // final values to install/upgrade with
	BaseValues  *helm.Values `json:"baseValues,omitempty"`  // existing release values when upgrading
	ChangedKeys []string     `json:"changedKeys,omitempty"` // dotted paths where Values differs from BaseValues
	Version     string       `json:"version,omitempty"`     // chart/manager version the apply must use; empty means the client's own
	Notes       []Note       `json:"notes,omitempty"`
}

type releaseAge int

const (
	releaseAbsent releaseAge = iota
	releaseOlder
	releaseSame
	releaseNewer
	releaseUnparseable
)

// releaseAge relates the installed release's version to clientVersion.
func (f *ClusterFacts) releaseAge(clientVersion semver.Version) releaseAge {
	if !f.Release.Installed {
		return releaseAbsent
	}
	v, err := semver.ParseTolerant(f.Release.Version)
	if err != nil {
		return releaseUnparseable
	}
	switch {
	case v.LT(clientVersion):
		return releaseOlder
	case v.GT(clientVersion):
		return releaseNewer
	default:
		return releaseSame
	}
}

// engine carries the values every recommendation step reads and the notes
// they accumulate, so a step's signature only names what varies per call.
type engine struct {
	facts    *ClusterFacts
	answers  Answers
	input    *helm.Values
	consult  ConsultFunc
	applying bool
	notes    notes
}

// Recommend is the pure decision engine: it turns probed facts and interview
// answers into a proposal, without any I/O or cluster access. It always runs
// as if applying, so a missing install privilege is a hard error; use
// RecommendWithInput directly to compute a proposal for validation-only runs.
func Recommend(facts *ClusterFacts, answers *Answers) (*Proposal, error) {
	return RecommendWithInput(facts, answers, &helm.Values{}, nil, true)
}

// RecommendWithInput is Recommend with an input values document whose settings
// are authoritative: each decision the engine makes that the input can pin is
// reconciled against the corresponding input field with decide/decideSlice/
// decideMap before the final values are merged over it, so the precedence is
// release values < input values < engine decisions for unpinned fields and
// consented changes.
//
// applying distinguishes an --apply run from a validation (--output-only or
// plain) run: a missing install privilege aborts an --apply run with an
// error, since the apply is about to fail anyway, but only becomes a warning
// note (plus handoff instructions) otherwise, so the proposal can still be
// computed and written for an admin to apply later.
func RecommendWithInput(facts *ClusterFacts, answers *Answers, input *helm.Values, consult ConsultFunc, applying bool) (*Proposal, error) {
	if err := facts.Release.ReadableValues(); err != nil {
		return nil, err
	}
	a := *answers
	if a.ManagedScope == "" {
		a.ManagedScope = ManagedScopeAll
	}
	e := &engine{facts: facts, answers: a, input: input, consult: consult, applying: applying}
	p := &Proposal{}

	// managed scope "all" installs the unrestricted chart; namespaces and
	// selector produce a namespace-limited install.
	clusterScope := a.ManagedScope == ManagedScopeAll
	if err := e.checkPrivileges(a.ManagedScope); err != nil {
		return nil, err
	}

	injector, nodeAgent, err := e.agentMachinery()
	if err != nil {
		return nil, err
	}
	if injector, err = decide(e, "agentInjector.enabled", input.AgentInjector.Enabled, injector); err != nil {
		return nil, err
	}
	if nodeAgent, err = decide(e, "nodeAgent.enabled", input.NodeAgent.Enabled, nodeAgent); err != nil {
		return nil, err
	}

	rec := &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(injector)},
		NodeAgent:     helm.NodeAgent{Enabled: new(nodeAgent)},
	}

	quic, err := e.quicValues(clusterScope)
	if err != nil {
		return nil, err
	}
	rec.QuicTunnel = quic

	if nodeAgent {
		for _, r := range facts.NodeAgent.Runtimes {
			switch r {
			case "containerd", "cri-o":
			case "docker":
				e.notes.warn("the docker container runtime requires nodeAgent.criSocket to point at the node's cri-dockerd socket")
			default:
				e.notes.warn(fmt.Sprintf("container runtime %q may require nodeAgent.criSocket to be set explicitly", r))
			}
		}
	}

	switch a.ManagedScope {
	case ManagedScopeNamespaces:
		nss, err := decideSlice(e, "namespaces", input.Namespaces, a.ManagedNamespaces)
		if err != nil {
			return nil, err
		}
		rec.Namespaces = nss
	case ManagedScopeSelector:
		labels, err := decideMap(e, "namespaceSelector.matchLabels", input.NamespaceSelector.MatchLabels, a.SelectorLabels)
		if err != nil {
			return nil, err
		}
		rec.NamespaceSelector = meta.LabelSelector{MatchLabels: labels}
		e.notes.info("a namespaceSelector uses one watcher per selected namespace up to maxNamespaceSpecificWatchers (default 10) " +
			"before switching to cluster-wide watchers")
	case ManagedScopeAll:
	}

	// The mapped-namespaces client default is independent of the managed
	// scope: a namespace-limited manager and a client-side mapped-namespaces
	// default can both be set at once.
	if len(a.MappedNamespaces) > 0 {
		mapped, err := decideSlice(e, "client.cluster.mappedNamespaces", input.Client.Cluster.MappedNamespaces, a.MappedNamespaces)
		if err != nil {
			return nil, err
		}
		rec.Client.Cluster = helm.ClientCluster{MappedNamespaces: mapped}
		e.notes.info("clients receive these namespaces as their mapped-namespaces default; a local --mapped-namespaces flag or config setting overrides it")
	}

	if conflicts := facts.Routing.ConflictingSubnets(); len(conflicts) > 0 {
		if err := e.conflictValues(rec, conflicts); err != nil {
			return nil, err
		}
	}

	if facts.ClientUpdate.UpdateAvailable {
		e.notes.info(fmt.Sprintf("a newer telepresence client %s is available", facts.ClientUpdate.Latest))
	}
	if injector && facts.Webhook.ReachabilityConcern != "" {
		e.notes.warn(facts.Webhook.ReachabilityConcern)
	}

	if err := e.securityValues(rec); err != nil {
		return nil, err
	}

	final := helm.MergeValues(rec, input)

	e.decideAction(final, p)
	// p.Values is nil exactly when decideAction proposed nothing (the
	// installed traffic-manager is newer than this client); the privilege
	// checks below have nothing to check in that case.
	if p.Values != nil {
		if err := e.checkX509Privileges(p.Values); err != nil {
			return nil, err
		}
		if err := e.checkCertManagerPrivileges(p.Values); err != nil {
			return nil, err
		}
	}
	p.Notes = e.notes.list
	return p, nil
}

// decide reconciles one scalar decision against the input's pinned field: an
// unset input adopts recommended, an equal one needs nothing, and a
// differing one asks e.consult (nil keeps the input and records a warning
// note).
func decide[T comparable](e *engine, path string, input *T, recommended T) (T, error) {
	if input == nil {
		return recommended, nil
	}
	if *input == recommended {
		return recommended, nil
	}
	if e.consult == nil {
		e.notes.warn(fmt.Sprintf("the input pins %s=%v; the recommendation %v was not applied", path, *input, recommended))
		return *input, nil
	}
	keep, err := e.consult(path, fmt.Sprint(*input), fmt.Sprint(recommended))
	if err != nil {
		return recommended, err
	}
	if keep {
		return *input, nil
	}
	return recommended, nil
}

// decideSlice is decide for a []string decision, compared with slices.Equal.
func decideSlice(e *engine, path string, input, recommended []string) ([]string, error) {
	if input == nil {
		return recommended, nil
	}
	if slices.Equal(input, recommended) {
		return recommended, nil
	}
	if e.consult == nil {
		e.notes.warn(fmt.Sprintf("the input pins %s=%v; the recommendation %v was not applied", path, input, recommended))
		return input, nil
	}
	keep, err := e.consult(path, fmt.Sprint(input), fmt.Sprint(recommended))
	if err != nil {
		return recommended, err
	}
	if keep {
		return input, nil
	}
	return recommended, nil
}

// decideMap is decide for a map[string]string decision, compared with maps.Equal.
func decideMap(e *engine, path string, input, recommended map[string]string) (map[string]string, error) {
	if input == nil {
		return recommended, nil
	}
	if maps.Equal(input, recommended) {
		return recommended, nil
	}
	if e.consult == nil {
		e.notes.warn(fmt.Sprintf("the input pins %s=%v; the recommendation %v was not applied", path, input, recommended))
		return input, nil
	}
	keep, err := e.consult(path, fmt.Sprint(input), fmt.Sprint(recommended))
	if err != nil {
		return recommended, err
	}
	if keep {
		return input, nil
	}
	return recommended, nil
}

// conflictValues emits the chosen routing-conflict strategy for conflicts,
// the cluster subnets that overlap a local route: virtual maps them through
// a VNAT (the chart default), and allow sends them to the cluster.
func (e *engine) conflictValues(rec *helm.Values, conflicts []string) error {
	switch e.answers.Conflicts {
	case ConflictsAllow:
		allowed, err := decideSlice(e, "client.routing.allowConflictingSubnets", e.input.Client.Routing.AllowConflictingSubnets, conflicts)
		if err != nil {
			return err
		}
		rec.Client.Routing = helm.ClientRouting{AllowConflictingSubnets: allowed}
		e.notes.info(fmt.Sprintf("clients accept local route conflicts with %s; traffic to those ranges goes to the cluster",
			strings.Join(conflicts, ", ")))
	default:
		resolve, err := decide(e, "client.routing.autoResolveConflicts", e.input.Client.Routing.AutoResolveConflicts, true)
		if err != nil {
			return err
		}
		rec.Client.Routing = helm.ClientRouting{AutoResolveConflicts: new(resolve)}
		e.notes.info(fmt.Sprintf("clients map the conflicting subnets (%s) to a virtual subnet; no --vnat flag is needed",
			strings.Join(conflicts, ", ")))
	}
	return nil
}

// securityValues emits security.authentication.mode, externalEndpoint.enabled,
// and clientRbac.legacyAccess, plus security.authorization.requiredGrant and
// the rest of the externalEndpoint block when enforcing, with their notes.
func (e *engine) securityValues(vals *helm.Values) error {
	mode := "permissive"
	if e.answers.EnforceAuth {
		mode = helm.AuthModeEnforcing
	}
	mode, err := decide(e, "security.authentication.mode", e.input.Security.Authentication.Mode, mode)
	if err != nil {
		return err
	}
	vals.Security = helm.Security{Authentication: helm.Authentication{Mode: new(mode)}}

	if e.answers.EnforceAuth {
		grant := e.answers.RequiredGrant
		if grant == "" {
			grant = RequiredGrantAny
		}
		grant, err = decide(e, "security.authorization.requiredGrant", e.input.Security.Authorization.RequiredGrant, grant)
		if err != nil {
			return err
		}
		vals.Security.Authorization = helm.Authorization{RequiredGrant: new(grant)}

		if !e.answers.ExternalEndpoint {
			if !e.facts.External.CertManager.Verdict.Likely() && len(e.facts.External.TLSSecrets) == 0 {
				e.notes.info("Direct Connect (an external control endpoint) needs a kubernetes.io/tls Secret in the manager namespace or " +
					"cert-manager; neither was found, so none is proposed")
			}
		}
	}

	extEnabled, err := decide(e, "externalEndpoint.enabled", e.input.ExternalEndpoint.Enabled, e.answers.ExternalEndpoint)
	if err != nil {
		return err
	}
	vals.ExternalEndpoint = helm.ExternalEndpoint{Enabled: new(extEnabled)}
	if e.answers.ExternalEndpoint {
		if err := e.externalEndpointValues(vals); err != nil {
			return err
		}
	}

	legacy := e.answers.LegacyAccess
	if e.apiPortOverridden() {
		if !legacy {
			e.notes.warn("apiPort is overridden, so clients cannot use the known-name connection; clientRbac.legacyAccess stays true")
		}
		legacy = true
	}
	legacy, err = decide(e, "clientRbac.legacyAccess", e.input.ClientRbac.LegacyAccess, legacy)
	if err != nil {
		return err
	}
	vals.ClientRbac = helm.ClientRbac{LegacyAccess: new(legacy)}
	return nil
}

// externalEndpointValues emits the chosen endpoint's service type and
// certificate source, leaving either alone when the installed release already
// sets it, and warns when QUIC ends up disabled alongside the endpoint.
func (e *engine) externalEndpointValues(vals *helm.Values) error {
	if e.facts.Release.Values.ExternalEndpoint.Service.Type == nil {
		serviceType := "NodePort"
		if lb := e.facts.Quic.LoadBalancer.Verdict; lb.Likely() {
			serviceType = "LoadBalancer"
		} else {
			e.notes.info("the external endpoint uses a NodePort service; its DNS names must resolve to a node address")
		}
		serviceType, err := decide(e, "externalEndpoint.service.type", e.input.ExternalEndpoint.Service.Type, serviceType)
		if err != nil {
			return err
		}
		vals.ExternalEndpoint.Service = helm.ExternalService{Type: new(serviceType)}
	}

	switch cma := e.answers.ExternalCertManager; {
	case cma != nil:
		vals.ExternalEndpoint.TLS = helm.ExternalTLS{CertManager: helm.CertManager{
			Enabled:   new(true),
			IssuerRef: helm.IssuerRef{Name: new(cma.IssuerName), Kind: new(cma.IssuerKind)},
			DNSNames:  cma.DNSNames,
		}}
	case e.answers.ExternalTLSSecret != "":
		secretName, err := decide(e, "externalEndpoint.tls.secretName", e.input.ExternalEndpoint.TLS.SecretName, e.answers.ExternalTLSSecret)
		if err != nil {
			return err
		}
		vals.ExternalEndpoint.TLS = helm.ExternalTLS{SecretName: new(secretName)}
	}

	if !deref(vals.QuicTunnel.Enabled) {
		e.notes.warn("attachments need the QUIC endpoint alongside Direct Connect; without it clients can connect but not intercept or ingest")
	}
	return nil
}

// apiPortOverridden reports whether the input or the installed release's
// values set apiPort to anything other than the chart's default (8081).
func (e *engine) apiPortOverridden() bool {
	for _, src := range []*helm.Values{e.input, e.facts.Release.Values} {
		if p := src.APIPort; p != nil && *p != 8081 {
			return true
		}
	}
	return false
}

// x509AuthEnabled reports whether values activate the traffic-manager's x509
// client-certificate auth listener: security.authentication.mode is
// "enforcing" and security.authentication.x509.enabled is not explicitly
// false (absent means enabled). It mirrors the chart's
// telepresence.x509AuthEnabled helper.
func x509AuthEnabled(values *helm.Values) bool {
	if !values.AuthEnforced() {
		return false
	}
	p := values.Security.Authentication.X509.Enabled
	return p == nil || *p
}

// agentMachinery resolves the decision table for the agent deployment mode:
// which of the agent-injector webhook and the node-agent the values enable.
func (e *engine) agentMachinery() (injector, nodeAgent bool, err error) {
	a := &e.answers
	naVerdict := e.facts.NodeAgent.Viable.Verdict
	naViable := naVerdict.Likely()
	naUsable := naViable && a.NodeAgent != TriOff || a.NodeAgent == TriOn
	if a.NodeAgent == TriOn && !naViable {
		e.notes.warn(fmt.Sprintf("node-agent forced on although the probe concluded %q: %s",
			naVerdict, strings.Join(e.facts.NodeAgent.Viable.Evidence, "; ")))
	}

	switch {
	case !a.Attach:
	case naUsable && !a.Replace:
		nodeAgent = true
	case naUsable && a.Replace:
		nodeAgent, injector = true, true
	default:
		injector = true
	}
	if nodeAgent && naVerdict == VerdictProbable {
		e.notes.info(fmt.Sprintf("node-agent viability is probable, not confirmed: %s", strings.Join(e.facts.NodeAgent.Viable.Evidence, "; ")))
	}
	if injector {
		switch e.facts.Webhook.CanCreate.Verdict {
		case VerdictNo:
			return false, false, webhookDeniedError()
		case VerdictUnknown:
			e.notes.warn(fmt.Sprintf("could not verify permission to create the agent-injector webhook: %s",
				strings.Join(e.facts.Webhook.CanCreate.Evidence, "; ")))
		case VerdictYes, VerdictProbable:
		}
	}
	return injector, nodeAgent, nil
}

// decideAction relates the recommendation to the existing installation: a
// fresh install, an upgrade carrying the merged values, or nothing at all.
func (e *engine) decideAction(vals *helm.Values, p *Proposal) {
	switch age := e.facts.releaseAge(version.Structured); age {
	case releaseAbsent:
		p.Action = ActionInstall
		p.Values = vals
	case releaseNewer:
		p.Action = ActionNone
		e.notes.warn(fmt.Sprintf("the installed traffic-manager %s is newer than this client (%s); upgrade the client instead of downgrading the traffic-manager",
			e.facts.Release.Version, version.Version))
	default:
		if age == releaseUnparseable {
			e.notes.warn(fmt.Sprintf("the installed traffic-manager version %q could not be parsed; treating it as older than the client", e.facts.Release.Version))
		}
		p.BaseValues = e.facts.Release.Values
		p.Values = helm.MergeValues(vals, e.facts.Release.Values)
		p.ChangedKeys = helm.DiffValues(p.Values, e.facts.Release.Values)
		switch {
		case age != releaseSame && e.answers.UpgradeManager:
			p.Action = ActionUpgrade
		case len(p.ChangedKeys) > 0:
			p.Action = ActionUpgrade
			if age != releaseSame {
				p.Version = e.facts.Release.Version
				e.notes.info(fmt.Sprintf("the installed traffic-manager version %s is kept; only values change", e.facts.Release.Version))
			}
		default:
			p.Action = ActionNone
			e.notes.info("the installation is up to date; nothing to change")
		}
		if p.Action == ActionUpgrade && e.facts.Release.Workload == "Deployment" {
			e.notes.info("the upgrade migrates the traffic-manager from a Deployment to a StatefulSet; " +
				"there is a brief window with no ready manager, and client sessions re-establish afterwards")
		}
	}
}

// PrivilegeDenial returns the Finding relevant to an install at the given
// managed scope, together with its structured denials: the cluster-wide
// render's for scope all (and the empty default), the namespace-scoped
// render's otherwise.
func (f *ClusterFacts) PrivilegeDenial(scope ManagedScope) (Finding, []DeniedAttribute) {
	if scope == ManagedScopeAll || scope == "" {
		return f.Privileges.ClusterWide, f.Privileges.MissingAttributes
	}
	return f.Privileges.Namespaced, f.Privileges.MissingNamespacedAttributes
}

// setupHandoffNote explains how an admin can complete an install once the
// missing privileges named in the preceding warning are granted.
const setupHandoffNote = "an admin can complete this install: hand them the missing privileges listed above so they can be granted, " +
	"plus this file (run 'telepresence setup --output FILE' to produce it), then have them run " +
	"'telepresence setup --input FILE --apply'"

// privilegeDenial implements the verdict handling shared by every install
// privilege check: VerdictNo is an error when applying, else a warning plus
// the handoff note; VerdictUnknown reports unknownFormat as a warning
// (unknownIsWarning) or info note; VerdictYes and VerdictProbable do nothing.
func (e *engine) privilegeDenial(finding Finding, deny func() error, unknownIsWarning bool, unknownFormat string) error {
	switch finding.Verdict {
	case VerdictNo:
		err := deny()
		if e.applying {
			return err
		}
		e.notes.warn(err.Error())
		e.notes.info(setupHandoffNote)
	case VerdictUnknown:
		text := fmt.Sprintf(unknownFormat, strings.Join(finding.Evidence, "; "))
		if unknownIsWarning {
			e.notes.warn(text)
		} else {
			e.notes.info(text)
		}
	case VerdictYes, VerdictProbable:
	}
	return nil
}

// checkPrivileges turns the P1 facts into an error when the chosen managed
// scope's install is known to be denied and the run is applying, a warning
// note (with handoff instructions) when it is denied but the run is only
// validating, or a warning note when the privileges could not be verified at
// all.
func (e *engine) checkPrivileges(scope ManagedScope) error {
	finding, _ := e.facts.PrivilegeDenial(scope)
	return e.privilegeDenial(finding, func() error { return e.facts.privilegeDeniedError(scope) }, true,
		"install privileges could not be verified: %s")
}

// checkX509Privileges is checkPrivileges for the kube-system RoleBinding
// x509 client-certificate authentication needs: a no-op unless values enable
// x509 auth, in which case a denial becomes an error when applying, a
// warning note (with the same handoff instructions as checkPrivileges) when
// only validating, and an unverifiable result becomes an info note.
func (e *engine) checkX509Privileges(values *helm.Values) error {
	if !x509AuthEnabled(values) {
		return nil
	}
	finding := e.facts.Privileges.X509KubeSystem
	return e.privilegeDenial(finding, func() error { return x509PrivilegeDeniedError(finding) }, false,
		"the kube-system RoleBinding needed for x509 client-certificate authentication could not be verified: %s")
}

// checkCertManagerPrivileges is a no-op unless values choose the
// cert-manager path for the external endpoint, in which case a denial
// becomes an error when applying, a warning plus handoff notes otherwise,
// and an unverifiable result becomes an info note.
func (e *engine) checkCertManagerPrivileges(values *helm.Values) error {
	if !deref(values.ExternalEndpoint.TLS.CertManager.Enabled) {
		return nil
	}
	finding := e.facts.Privileges.CertManagerCertificate
	return e.privilegeDenial(finding, func() error { return certManagerPrivilegeDeniedError(finding) }, false,
		"the cert-manager certificate privilege for the external endpoint could not be verified: %s")
}

// x509PrivilegeDeniedError names the missing kube-system RoleBinding
// privilege x509 client-certificate authentication needs.
func x509PrivilegeDeniedError(finding Finding) error {
	return errcat.User.New("insufficient privileges for x509 client-certificate authentication; missing:\n  " +
		strings.Join(finding.Evidence, "\n  "))
}

// certManagerPrivilegeDeniedError names the missing privilege for the
// external endpoint's cert-manager certificate.
func certManagerPrivilegeDeniedError(finding Finding) error {
	return errcat.User.New("insufficient privileges to create the external endpoint's cert-manager certificate; missing:\n  " +
		strings.Join(finding.Evidence, "\n  "))
}

// privilegeDeniedError names the missing-privilege error for the given
// managed scope.
func (f *ClusterFacts) privilegeDeniedError(scope ManagedScope) error {
	if scope == ManagedScopeAll || scope == "" {
		return f.clusterWideDeniedError()
	}
	return errcat.User.New("insufficient privileges for a namespace-limited install; missing:\n  " +
		strings.Join(f.Privileges.MissingNamespaced, "\n  "))
}

// webhookDeniedError names the hard incompatibility between wanting the
// agent-injector and lacking the privilege to create its webhook.
func webhookDeniedError() error {
	return errcat.User.New(
		"the agent-injector webhook is required, but creating mutatingwebhookconfigurations.admissionregistration.k8s.io is not permitted")
}

// clusterWideDeniedError itemizes why a cluster-wide install is not permitted
// and points at the namespace-limited fallback when the probes concluded it
// would work.
func (f *ClusterFacts) clusterWideDeniedError() error {
	pf := &f.Privileges
	msg := "insufficient privileges for a cluster-wide install; missing:\n  " + strings.Join(pf.Missing, "\n  ")
	switch pf.Namespaced.Verdict {
	case VerdictYes:
		msg += "\na namespace-limited install (--managed-scope=namespaces) appears possible"
	case VerdictNo:
		msg += "\na namespace-limited install is also not permitted; missing:\n  " + strings.Join(pf.MissingNamespaced, "\n  ")
	case VerdictProbable, VerdictUnknown:
	}
	return errcat.User.New(msg)
}

// quicValues decides quicTunnel.enabled and its service type, reconciling
// both against the input, and returns the resulting QuicTunnel block. QUIC is
// never enabled over an existing installation that runs more than one
// traffic-manager replica.
func (e *engine) quicValues(clusterScope bool) (helm.QuicTunnel, error) {
	lb := e.facts.Quic.LoadBalancer.Verdict
	np := e.facts.Quic.NodePort.Verdict
	nodePortViable := np.Likely()

	enabled := false
	serviceType := ""
	switch e.answers.Quic {
	case TriOff:
	case TriOn:
		enabled = true
		if lb == VerdictNo && nodePortViable && clusterScope {
			serviceType = "NodePort"
		}
	default:
		switch {
		case lb.Likely():
			enabled = true
			e.notes.info("QUIC enabled with the default LoadBalancer service: " + strings.Join(e.facts.Quic.LoadBalancer.Evidence, "; "))
		case nodePortViable && clusterScope:
			enabled = true
			serviceType = "NodePort"
			e.notes.info("QUIC enabled with a NodePort service; the traffic-manager needs cluster-wide node access for NodePort discovery")
		default:
			e.notes.info(fmt.Sprintf("QUIC disabled: no viable service type (loadBalancer %s, nodePort %s)", lb, np))
		}
	}

	if enabled {
		if rc := e.facts.Release.Values.ReplicaCount; rc != nil && *rc > 1 {
			enabled = false
			serviceType = ""
			e.notes.warn(fmt.Sprintf("quicTunnel requires replicaCount 1, but the existing installation uses replicaCount %d; QUIC stays disabled", *rc))
		}
	}

	enabled, err := decide(e, "quicTunnel.enabled", e.input.QuicTunnel.Enabled, enabled)
	if err != nil {
		return helm.QuicTunnel{}, err
	}
	serviceType, err = decide(e, "quicTunnel.service.type", e.input.QuicTunnel.Service.Type, serviceType)
	if err != nil {
		return helm.QuicTunnel{}, err
	}

	quic := helm.QuicTunnel{Enabled: new(enabled)}
	if serviceType != "" {
		quic.Service = helm.QuicTunnelService{Type: new(serviceType)}
	}
	return quic, nil
}
