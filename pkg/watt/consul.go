package watt

import (
	"fmt"

	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/supervisor"
	consulapi "github.com/hashicorp/consul/api"
)

type ConsulServiceNodeWatchMaker struct {
	ConsulAddress string `json:""`
	Service       string `json:""`
	Datacenter    string `json:""`
	OnlyHealthy   bool   `json:""`
}

func (m *ConsulServiceNodeWatchMaker) ID() string {
	return fmt.Sprintf("%s/%s", m.Datacenter, m.Service)
}

func (m *ConsulServiceNodeWatchMaker) Make(notify chan<- consulwatch.Endpoints) (func(p *supervisor.Process) error, error) {
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = m.ConsulAddress
	consul, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, err
	}

	return func(p *supervisor.Process) error {
		var err error

		// TODO: Pass a logger to Consul but right now we don't have one available from the supervisor.
		// The consulwatch lib will initialize a default logger but if we want sane formatting that's not a good long
		// term solution.
		serviceWatcher, err := consulwatch.New(consul, nil, m.Service, m.OnlyHealthy)
		if err != nil {
			p.Logf("error setting up new consul watch %v", err)
			return err
		}

		serviceWatcher.Watch(func(endpoints consulwatch.Endpoints, e error) { notify <- endpoints })
		p.Go(func(p *supervisor.Process) error {
			err = serviceWatcher.Start()
			if err != nil {
				p.Logf("failed to start service watcher %v", err)
				return err
			}
			return nil
		})

		<-p.Shutdown()
		serviceWatcher.Stop()
		return nil
	}, nil
}
