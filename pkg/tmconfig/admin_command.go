package tmconfig

import (
	"github.com/go-json-experiment/json"
	"sigs.k8s.io/yaml"
)

type CommandName string

const (
	RemoveIntercept CommandName = "removeIntercept"
)

type AdminCommand struct {
	Name CommandName `json:"name"`
	Args []string    `json:"args"`

	// Timestamp expressed as nanoseconds since the Unix epoch.
	Timestamp int64 `json:"timestamp"`
}

type AdminCommandList []AdminCommand

func (l AdminCommandList) MarshalYAML() ([]byte, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(data)
}

//goland:noinspection GoMixedReceiverTypes
func (l *AdminCommandList) UnmarshalYAML(data []byte) error {
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, l)
}
