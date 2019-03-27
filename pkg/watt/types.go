package watt

import (
	"encoding/json"

	"github.com/datawire/teleproxy/pkg/consulwatch"

	"github.com/datawire/teleproxy/pkg/k8s"
)

type ConsulSnapshot struct {
	Endpoints map[string]consulwatch.Endpoints `json:",omitempty"`
}

func (s *ConsulSnapshot) DeepCopy() (*ConsulSnapshot, error) {
	jsonBytes, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	res := &ConsulSnapshot{}
	err = json.Unmarshal(jsonBytes, res)

	return res, err
}

type Snapshot struct {
	Consul     ConsulSnapshot            `json:",omitempty"`
	Kubernetes map[string][]k8s.Resource `json:",omitempty"`
}
