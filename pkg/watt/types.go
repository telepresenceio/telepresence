package watt

import (
	"encoding/json"
	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
)

type ConsulSnapshot struct {
	Endpoints []consulwatch.Endpoints `json:",omitempty"`
}

func (s *ConsulSnapshot) DeepCopy() (*ConsulSnapshot, error) {
	jsonBytes, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	res := &ConsulSnapshot{}
	err = json.Unmarshal(jsonBytes, res)

	return res, nil
}

type Snapshot struct {
	Consul     ConsulSnapshot `json:""`
	Kubernetes []k8s.Resource `json:""`
}
