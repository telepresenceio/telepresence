package setup

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/blang/semver/v4"
	"helm.sh/helm/v3/pkg/chartutil"

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

// Proposal is the outcome of the decision engine: the values to install or
// upgrade with, the planned action, and the notes explaining both.
type Proposal struct {
	Action           Action         `json:"action"`
	Values           map[string]any `json:"values"`                     // final values to install/upgrade with
	BaseValues       map[string]any `json:"baseValues,omitempty"`       // existing release values when upgrading
	ChangedKeys      []string       `json:"changedKeys,omitempty"`      // dotted paths where Values differs from BaseValues
	MappedNamespaces []string       `json:"mappedNamespaces,omitempty"` // scope=mapped: goes to a WorkstationState snippet, not helm
	Notes            []Note         `json:"notes,omitempty"`
}

type releaseAge int

const (
	releaseAbsent releaseAge = iota
	releaseOlder
	releaseSame
	releaseNewer
	releaseUnparseable
)

// compareRelease relates the installed release's version to the client's.
func compareRelease(facts *ClusterFacts, clientVersion semver.Version) releaseAge {
	if !facts.Release.Installed {
		return releaseAbsent
	}
	v, err := semver.ParseTolerant(facts.Release.Version)
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

// Recommend is the pure decision engine: it turns probed facts and interview
// answers into a proposal, without any I/O or cluster access.
func Recommend(facts *ClusterFacts, answers *Answers) (*Proposal, error) {
	a := *answers
	if a.Scope == "" {
		a.Scope = ScopeAll
	}
	p := &Proposal{}
	info := func(format string, args ...any) {
		p.Notes = append(p.Notes, Note{Level: NoteInfo, Text: fmt.Sprintf(format, args...)})
	}
	warn := func(format string, args ...any) {
		p.Notes = append(p.Notes, Note{Level: NoteWarning, Text: fmt.Sprintf(format, args...)})
	}

	// scope all and mapped both install the unrestricted chart; only
	// namespaces and selector produce a namespace-limited install.
	clusterScope := a.Scope == ScopeAll || a.Scope == ScopeMapped
	if err := checkPrivileges(facts, clusterScope, warn); err != nil {
		return nil, err
	}

	injector, nodeAgent, err := agentMachinery(facts, &a, info, warn)
	if err != nil {
		return nil, err
	}
	vals := map[string]any{
		"agentInjector": map[string]any{"enabled": injector},
		"nodeAgent":     map[string]any{"enabled": nodeAgent},
	}

	quicValues(facts, &a, clusterScope, vals, info, warn)

	if nodeAgent {
		for _, r := range facts.NodeAgent.Runtimes {
			switch r {
			case "containerd", "cri-o":
			case "docker":
				warn("the docker container runtime requires nodeAgent.criSocket to point at the node's cri-dockerd socket")
			default:
				warn("container runtime %q may require nodeAgent.criSocket to be set explicitly", r)
			}
		}
	}

	switch a.Scope {
	case ScopeNamespaces:
		nss := make([]any, len(a.ManagedNamespaces))
		for i, ns := range a.ManagedNamespaces {
			nss[i] = ns
		}
		vals["namespaces"] = nss
	case ScopeSelector:
		matchLabels := make(map[string]any, len(a.SelectorLabels))
		for k, v := range a.SelectorLabels {
			matchLabels[k] = v
		}
		vals["namespaceSelector"] = map[string]any{"matchLabels": matchLabels}
		info("a namespaceSelector uses one watcher per selected namespace up to maxNamespaceSpecificWatchers (default 10) before switching to cluster-wide watchers")
	case ScopeMapped:
		p.MappedNamespaces = a.ManagedNamespaces
	case ScopeAll:
	}

	if facts.ClientUpdate.UpdateAvailable {
		info("a newer telepresence client %s is available", facts.ClientUpdate.Latest)
	}
	if injector && facts.Webhook.ReachabilityConcern != "" {
		warn("%s", facts.Webhook.ReachabilityConcern)
	}

	decideAction(facts, &a, vals, p, info, warn)
	return p, nil
}

// agentMachinery resolves the decision table for the agent deployment mode:
// which of the agent-injector webhook and the node-agent the values enable.
func agentMachinery(facts *ClusterFacts, a *Answers, info, warn func(string, ...any)) (injector, nodeAgent bool, err error) {
	naVerdict := facts.NodeAgent.Viable.Verdict
	naViable := naVerdict == VerdictYes || naVerdict == VerdictProbable
	naUsable := naViable && a.NodeAgent != TriOff || a.NodeAgent == TriOn
	if a.NodeAgent == TriOn && !naViable {
		warn("node-agent forced on although the probe concluded %q: %s",
			naVerdict, strings.Join(facts.NodeAgent.Viable.Evidence, "; "))
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
		info("node-agent viability is probable, not confirmed: %s", strings.Join(facts.NodeAgent.Viable.Evidence, "; "))
	}
	if injector {
		switch facts.Webhook.CanCreate.Verdict {
		case VerdictNo:
			return false, false, errcat.User.New(
				"the agent-injector webhook is required, but creating mutatingwebhookconfigurations.admissionregistration.k8s.io is not permitted")
		case VerdictUnknown:
			warn("could not verify permission to create the agent-injector webhook: %s",
				strings.Join(facts.Webhook.CanCreate.Evidence, "; "))
		case VerdictYes, VerdictProbable:
		}
	}
	return injector, nodeAgent, nil
}

// decideAction relates the recommendation to the existing installation: a
// fresh install, an upgrade carrying the merged values, or nothing at all.
func decideAction(facts *ClusterFacts, a *Answers, vals map[string]any, p *Proposal, info, warn func(string, ...any)) {
	switch age := compareRelease(facts, version.Structured); age {
	case releaseAbsent:
		p.Action = ActionInstall
		p.Values = vals
	case releaseNewer:
		p.Action = ActionNone
		warn("the installed traffic-manager %s is newer than this client (%s); upgrade the client instead of downgrading the traffic-manager",
			facts.Release.Version, version.Version)
	default:
		if age == releaseUnparseable {
			warn("the installed traffic-manager version %q could not be parsed; treating it as older than the client", facts.Release.Version)
		}
		p.BaseValues = facts.Release.Values
		p.Values = chartutil.CoalesceTables(vals, facts.Release.Values)
		p.ChangedKeys = diffKeys(p.Values, facts.Release.Values, "")
		switch {
		case age != releaseSame && a.UpgradeManager:
			p.Action = ActionUpgrade
		case len(p.ChangedKeys) > 0:
			p.Action = ActionUpgrade
			if age != releaseSame {
				info("the installed traffic-manager version %s is kept; only values change", facts.Release.Version)
			}
		default:
			p.Action = ActionNone
			info("the installation is up to date; nothing to change")
		}
	}
}

// checkPrivileges turns the P1 facts into an error when the chosen scope's
// install is known to be denied, or a warning note when it could not be
// verified.
func checkPrivileges(facts *ClusterFacts, clusterScope bool, warn func(string, ...any)) error {
	pf := &facts.Privileges
	if clusterScope {
		switch pf.ClusterWide.Verdict {
		case VerdictNo:
			msg := "insufficient privileges for a cluster-wide install; missing:\n  " + strings.Join(pf.Missing, "\n  ")
			switch pf.Namespaced.Verdict {
			case VerdictYes:
				msg += "\na namespace-limited install (--scope=namespaces) appears possible"
			case VerdictNo:
				msg += "\na namespace-limited install is also not permitted; missing:\n  " + strings.Join(pf.MissingNamespaced, "\n  ")
			case VerdictProbable, VerdictUnknown:
			}
			return errcat.User.New(msg)
		case VerdictUnknown:
			warn("install privileges could not be verified: %s", strings.Join(pf.ClusterWide.Evidence, "; "))
		case VerdictYes, VerdictProbable:
		}
		return nil
	}
	switch pf.Namespaced.Verdict {
	case VerdictNo:
		return errcat.User.New("insufficient privileges for a namespace-limited install; missing:\n  " + strings.Join(pf.MissingNamespaced, "\n  "))
	case VerdictUnknown:
		warn("install privileges could not be verified: %s", strings.Join(pf.Namespaced.Evidence, "; "))
	case VerdictYes, VerdictProbable:
	}
	return nil
}

// quicValues decides quicTunnel.enabled and its service type and adds them to
// vals. QUIC is never enabled over an existing installation that runs more
// than one traffic-manager replica.
func quicValues(facts *ClusterFacts, a *Answers, clusterScope bool, vals map[string]any, info, warn func(string, ...any)) {
	lb := facts.Quic.LoadBalancer.Verdict
	np := facts.Quic.NodePort.Verdict
	nodePortViable := np == VerdictProbable || np == VerdictYes

	enabled := false
	serviceType := ""
	switch a.Quic {
	case TriOff:
	case TriOn:
		enabled = true
		if lb == VerdictNo && nodePortViable && clusterScope {
			serviceType = "NodePort"
		}
	default:
		switch {
		case lb == VerdictYes || lb == VerdictProbable:
			enabled = true
			info("QUIC enabled with the default LoadBalancer service: %s", strings.Join(facts.Quic.LoadBalancer.Evidence, "; "))
		case nodePortViable && clusterScope:
			enabled = true
			serviceType = "NodePort"
			info("QUIC enabled with a NodePort service; the traffic-manager needs cluster-wide node access for NodePort discovery")
		default:
			info("QUIC disabled: no viable service type (loadBalancer %s, nodePort %s)", lb, np)
		}
	}

	if enabled {
		if rc, ok := numericValue(facts.Release.Values["replicaCount"]); ok && rc > 1 {
			enabled = false
			serviceType = ""
			warn("quicTunnel requires replicaCount 1, but the existing installation uses replicaCount %d; QUIC stays disabled", rc)
		}
	}
	quic := map[string]any{"enabled": enabled}
	if serviceType != "" {
		quic["service"] = map[string]any{"type": serviceType}
	}
	vals["quicTunnel"] = quic
}

// numericValue coerces the numeric types that YAML/JSON decoding produces.
func numericValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// diffKeys returns the dotted paths at which final differs from base. Keys
// present only in base never differ: the merge preserved them verbatim.
func diffKeys(final, base map[string]any, prefix string) []string {
	keys := make([]string, 0, len(final))
	for k := range final {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []string
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		fv := final[k]
		bv, inBase := base[k]
		fm, fIsMap := fv.(map[string]any)
		bm, bIsMap := bv.(map[string]any)
		switch {
		case fIsMap && bIsMap:
			out = append(out, diffKeys(fm, bm, path)...)
		case fIsMap:
			out = append(out, diffKeys(fm, map[string]any{}, path)...)
		case !inBase || !reflect.DeepEqual(fv, bv):
			out = append(out, path)
		}
	}
	return out
}
