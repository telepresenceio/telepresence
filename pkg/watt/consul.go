package watt

import (
	"fmt"
	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	consulapi "github.com/hashicorp/consul/api"
)

type ConsulServiceNodeWatchMaker struct {
	Service     string `json:""`
	Datacenter  string `json:""`
	OnlyHealthy bool   `json:""`
}

func (m *ConsulServiceNodeWatchMaker) ID() string {
	return fmt.Sprintf("%s/%s", m.Datacenter, m.Service)
}

func (m *ConsulServiceNodeWatchMaker) Make(notify chan<- []k8s.Resource) (func(p *supervisor.Process) error, error) {
	consulConfig := consulapi.DefaultConfig()
	consul, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, err
	}

	return func(p *supervisor.Process) error {
		var err error

		serviceWatcher, err := consulwatch.New(consul, m.Service, m.OnlyHealthy)
		if err != nil {
			return err
		}

		serviceWatcher.Watch(func(endpoints consulwatch.Endpoints, e error) {
			if e != nil {
				err = e
			}

			fmt.Printf("records count=%d\n", len(endpoints.Endpoints))
			addresses := make([]string, 0)
			port := 0
			for _, e := range endpoints.Endpoints {
				addresses = append(addresses, e.Address)
				port = e.Port
			}

			consulResource := make(map[string]interface{})
			consulResource["kind"] = "Endpoints"
			consulResource["apiVersion"] = "v1"

			metadata := make(map[string]interface{})
			metadata["name"] = m.Service
			consulResource["metadata"] = metadata

			subset := make(map[string]interface{})
			subset["addresses"] = addresses
			subset["ports"] = []map[string]interface{}{
				{"port": port, "protocol": "TCP"},
			}

			consulResource["subsets"] = []map[string]interface{}{subset}

			notify <- []k8s.Resource{consulResource}
		})

		err = serviceWatcher.Start()

		return nil
	}, nil
}
