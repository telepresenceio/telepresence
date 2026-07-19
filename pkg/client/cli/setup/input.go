package setup

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// LoadInputValues reads a Helm values document (typically one produced by
// --output) whose settings become pinned defaults.
func LoadInputValues(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errcat.User.Errorf(err, "reading input values %q", path)
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, errcat.User.Errorf(err, "parsing input values %q", path)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

// Pins are the unambiguous answers an input values document implies; a pinned
// answer skips its interview question exactly like a flag-preset answer.
type Pins struct {
	Attach            *bool
	ReplaceDetermined bool // the replace question is moot or answered by the input
	Replace           bool
	Quic              *Tri // TriOn/TriOff from quicTunnel.enabled
	Scope             *ScopeChoice
	ManagedNamespaces []string
	SelectorLabels    map[string]string
}

// DerivePins derives the answer pins from an input values document. Only keys
// present in the input pin anything; a setting whose implication is ambiguous
// (e.g. nodeAgent.enabled=false alone) pins nothing and is instead protected
// by the reconcile pass.
func DerivePins(values map[string]any) Pins {
	var pins Pins
	if len(values) == 0 {
		return pins
	}

	injector, iPresent := boolAt(values, "agentInjector", "enabled")
	nodeAgent, nPresent := boolAt(values, "nodeAgent", "enabled")
	switch {
	case iPresent && nPresent:
		attach := injector || nodeAgent
		pins.Attach = &attach
		if attach {
			pins.ReplaceDetermined = true
			pins.Replace = nodeAgent && injector
		}
	case nPresent && nodeAgent:
		attach := true
		pins.Attach = &attach
	case iPresent && injector:
		attach := true
		pins.Attach = &attach
		pins.ReplaceDetermined = true
	}

	if quic, present := boolAt(values, "quicTunnel", "enabled"); present {
		tri := TriOff
		if quic {
			tri = TriOn
		}
		pins.Quic = &tri
	}

	// Scope pins are matched in order; a "namespaces" key wins over the
	// mapped-namespaces client default, since combining them is nonsensical.
	switch {
	case pinScopeList(&pins, ScopeNamespaces, values, "namespaces"):
	case pinScopeSelector(&pins, values):
	case pinScopeList(&pins, ScopeMapped, values, "client", "cluster", "mappedNamespaces"):
	}
	return pins
}

func pinScopeList(pins *Pins, scope ScopeChoice, values map[string]any, path ...string) bool {
	nss, ok := stringListAt(values, path...)
	if !ok || len(nss) == 0 {
		return false
	}
	pins.Scope = &scope
	pins.ManagedNamespaces = nss
	return true
}

func pinScopeSelector(pins *Pins, values map[string]any) bool {
	labels, ok := stringMapAt(values, "namespaceSelector", "matchLabels")
	if !ok {
		return false
	}
	scope := ScopeSelector
	pins.Scope = &scope
	pins.SelectorLabels = labels
	return true
}

// ApplyTo transfers the pins into answers and presets so the interview skips
// the pinned questions. A flag-preset answer always wins over a pin; the QUIC
// override is pinned only while it is still "auto".
func (pins *Pins) ApplyTo(a *Answers, pre *Preset) {
	if pins.Attach != nil && !pre.Attach {
		a.Attach = *pins.Attach
		pre.Attach = true
	}
	if pins.ReplaceDetermined && !pre.Replace {
		a.Replace = pins.Replace
		pre.Replace = true
	}
	if pins.Quic != nil && (a.Quic == "" || a.Quic == TriAuto) {
		a.Quic = *pins.Quic
	}
	if pins.Scope != nil && !pre.Scope {
		a.Scope = *pins.Scope
		pre.Scope = true
		if len(pins.ManagedNamespaces) > 0 && !pre.ManagedNamespaces {
			a.ManagedNamespaces = slices.Clone(pins.ManagedNamespaces)
			pre.ManagedNamespaces = true
		}
		if len(pins.SelectorLabels) > 0 && len(a.SelectorLabels) == 0 {
			a.SelectorLabels = pins.SelectorLabels
		}
	}
}

// ConsultFunc resolves a conflict between an input-pinned value and the
// engine's recommendation; returning true keeps the input value.
type ConsultFunc func(key string, inputVal, recVal any) (bool, error)

// ConsultInput is the interactive ConsultFunc: it asks whether to keep the
// conflicting input value, defaulting to keep.
func (iv *Interviewer) ConsultInput(key string, inputVal, recVal any) (bool, error) {
	return iv.askYesNo(fmt.Sprintf("The input sets %s=%v; probing recommends %v. Keep the input value? [Y/n] ", key, inputVal, recVal), true)
}

// ReconcileWithInput merges the engine's recommendation into the input values
// without ever changing a pinned (input-present) setting silently: an absent
// key adopts the recommendation, an equal key needs nothing, and a conflict is
// put to consult. A nil consult keeps the input value and records a warning
// note instead (the non-interactive behavior). Input keys the engine has no
// opinion about pass through untouched.
func ReconcileWithInput(rec, input map[string]any, consult ConsultFunc) (map[string]any, []Note, error) {
	final := deepClone(input)
	var notes []Note
	err := reconcileWalk(rec, input, final, "", consult, &notes)
	if err != nil {
		return nil, nil, err
	}
	return final, notes, nil
}

func reconcileWalk(rec, input, final map[string]any, prefix string, consult ConsultFunc, notes *[]Note) error {
	keys := make([]string, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		rv := rec[k]
		iv, present := input[k]

		if rm, recIsMap := rv.(map[string]any); recIsMap {
			if im, inputIsMap := iv.(map[string]any); inputIsMap || !present {
				if !present {
					im = map[string]any{}
				}
				fm, ok := final[k].(map[string]any)
				if !ok {
					fm = map[string]any{}
					final[k] = fm
				}
				if err := reconcileWalk(rm, im, fm, path, consult, notes); err != nil {
					return err
				}
				continue
			}
		}

		switch {
		case !present:
			final[k] = rv
		case reflect.DeepEqual(iv, rv):
		default:
			keep := true
			if consult != nil {
				var err error
				if keep, err = consult(path, iv, rv); err != nil {
					return err
				}
			} else {
				*notes = append(*notes, Note{
					Level: NoteWarning,
					Text:  fmt.Sprintf("the input pins %s=%v; the recommendation %v was not applied", path, iv, rv),
				})
			}
			if !keep {
				final[k] = rv
			}
		}
	}
	return nil
}

// ValidateValues checks the final values, wherever they came from, against
// the hard incompatibilities the probes uncovered. Only keys present in the
// values participate.
func ValidateValues(facts *ClusterFacts, vals map[string]any) error {
	if injector, present := boolAt(vals, "agentInjector", "enabled"); present && injector {
		if facts.Webhook.CanCreate.Verdict == VerdictNo {
			return webhookDeniedError()
		}
	}
	_, hasNamespaces := vals["namespaces"]
	_, hasSelector := vals["namespaceSelector"]
	if !hasNamespaces && !hasSelector && facts.Privileges.ClusterWide.Verdict == VerdictNo {
		return clusterWideDeniedError(facts)
	}
	if quic, present := boolAt(vals, "quicTunnel", "enabled"); present && quic {
		if rc, ok := numericValue(vals["replicaCount"]); ok && rc > 1 {
			return errcat.User.Newf("quicTunnel.enabled requires replicaCount 1, but the values set replicaCount %d", rc)
		}
	}
	return nil
}

func deepClone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepClone(t)
		case []any:
			out[k] = slices.Clone(t)
		default:
			out[k] = v
		}
	}
	return out
}

func boolAt(m map[string]any, path ...string) (bool, bool) {
	v, present := valueAt(m, path...)
	if !present {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func stringListAt(m map[string]any, path ...string) ([]string, bool) {
	v, present := valueAt(m, path...)
	if !present {
		return nil, false
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func stringMapAt(m map[string]any, path ...string) (map[string]string, bool) {
	v, present := valueAt(m, path...)
	if !present {
		return nil, false
	}
	mm, ok := v.(map[string]any)
	if !ok || len(mm) == 0 {
		return nil, false
	}
	out := make(map[string]string, len(mm))
	for k, e := range mm {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[k] = s
	}
	return out, true
}

func valueAt(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}
