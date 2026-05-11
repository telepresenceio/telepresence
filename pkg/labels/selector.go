package labels

import (
	"encoding/json/v2"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/yaml"
)

const NameLabelKey = "kubernetes.io/metadata.name"

type Operator string

// These are the camel-cased operators that are valid in a kubernetes namespaceSelector.
//
// NOTE! The lowercase variants in the k8s.io/apimachinery/pkg/selection are intended for
// the selector string representation only. They are invalid in a YAML/JSON manifest!
const (
	OperatorIn        Operator = "In"
	OperatorNotIn     Operator = "NotIn"
	OperatorExists    Operator = "Exists"
	OperatorNotExists Operator = "DoesNotExist"
)

func (op Operator) String() string {
	return string(op)
}

func (op Operator) AsSelectionOperator() selection.Operator {
	return selection.Operator(strings.ToLower(string(op)))
}

type Selector struct {
	MatchLabels      map[string]string `json:"matchLabels,omitempty"`
	MatchExpressions []*Requirement    `json:"matchExpressions,omitempty"`
}

type Requirement struct {
	Key      string   `json:"key"`
	Operator Operator `json:"operator"`
	Values   []string `json:"values"`
}

func UnmarshalSelector(data []byte) (*Selector, error) {
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	return UnmarshalSelectorJSON(data)
}

func UnmarshalSelectorJSON(data []byte) (*Selector, error) {
	var nsSelector Selector
	err := json.Unmarshal(data, &nsSelector)
	return &nsSelector, err
}

// GetAllRequirements transforms the "<key>=<value>" MatchableLabels into "<key> in [<value>]"
// MatchRequirements and returns the sorted sum of all requirements.
func (sel *Selector) GetAllRequirements() []*Requirement {
	if sel == nil {
		return nil
	}
	es := sel.MatchExpressions
	for key, val := range sel.MatchLabels {
		es = append(es, &Requirement{
			Key:      key,
			Operator: OperatorIn,
			Values:   []string{val},
		})
	}
	slices.SortFunc(es, rqCmp)
	return es
}

func (sel *Selector) Static() bool {
	if sel == nil {
		return false
	}
	switch len(sel.MatchLabels) {
	case 0:
		if len(sel.MatchExpressions) == 1 {
			m := sel.MatchExpressions[0]
			return m.Key == NameLabelKey && m.Operator == OperatorIn
		}
	case 1:
		_, ok := sel.MatchLabels[NameLabelKey]
		return ok
	}
	return false
}

func (sel *Selector) StaticNames() []string {
	if sel == nil {
		return nil
	}
	switch len(sel.MatchLabels) {
	case 0:
		if len(sel.MatchExpressions) == 1 {
			m := sel.MatchExpressions[0]
			if m.Key == NameLabelKey && m.Operator == OperatorIn {
				return m.Values
			}
		}
	case 1:
		if n, ok := sel.MatchLabels[NameLabelKey]; ok {
			return []string{n}
		}
	}
	return nil
}

func (sel *Selector) LabelsSelector() (labels.Selector, error) {
	return NewLabelsSelector(sel.GetAllRequirements())
}

func NewLabelsSelector(es []*Requirement) (labels.Selector, error) {
	rqs := make([]labels.Requirement, 0, len(es))
	for _, ns := range es {
		rq, err := labels.NewRequirement(ns.Key, ns.Operator.AsSelectionOperator(), ns.Values)
		if err != nil {
			return nil, err
		}
		rqs = append(rqs, *rq)
	}
	return labels.NewSelector().Add(rqs...), nil
}

func rqCmp(a, b *Requirement) int {
	n := strings.Compare(a.Key, b.Key)
	if n == 0 {
		n = strings.Compare(string(a.Operator), string(b.Operator))
		if n == 0 {
			n = slices.Compare(a.Values, b.Values)
		}
	}
	return n
}

func SelectorFromNames(names ...string) *Selector {
	if len(names) == 0 {
		return nil
	}
	return &Selector{
		MatchExpressions: []*Requirement{{
			Key:      NameLabelKey,
			Operator: OperatorIn,
			Values:   names,
		}},
	}
}
