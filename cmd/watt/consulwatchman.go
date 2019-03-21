package main

import (
	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/watt"
)

type consulwatchman struct {
	watchesCh                 <-chan []k8s.Resource
	consulEndpointsAggregator chan<- consulwatch.Endpoints
	watched                   map[string]*supervisor.Worker
	ready                     bool
}

func (w *consulwatchman) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		select {
		case resources := <-w.watchesCh:
			found := make(map[string]*supervisor.Worker)
			p.Logf("processing %d kubernetes resources", len(resources))
			for _, r := range resources {
				if !isConsulResolver(r) {
					panic(r)
				}
				worker, err := w.makeConsulWatcher(r)
				if err != nil {
					p.Logf("failed to create consul watch %v", err)
					continue
				}

				if _, exists := w.watched[worker.Name]; !exists {
					p.Logf("add consul watcher %s\n", worker.Name)
					p.Supervisor().Supervise(worker)
					w.watched[worker.Name] = worker
				}

				found[worker.Name] = worker
			}

			// purge the watches that no longer are needed because they did not come through the in the latest
			// report
			for k, worker := range w.watched {
				if _, exists := found[k]; !exists {
					p.Logf("remove consul watcher %s\n", k)
					worker.Shutdown()
				}
			}

			w.watched = found
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

func (w *consulwatchman) makeConsulWatcher(r k8s.Resource) (*supervisor.Worker, error) {
	data := r.Data()
	cwm := &watt.ConsulServiceNodeWatchMaker{
		ConsulAddress: data["consulAddress"].(string),
		Service:       data["service"].(string),
		Datacenter:    data["datacenter"].(string),
		OnlyHealthy:   true,
	}

	cwmFunc, err := cwm.Make(w.consulEndpointsAggregator)
	if err != nil {
		return nil, err
	}

	return &supervisor.Worker{
		Name:  cwm.ID(),
		Work:  cwmFunc,
		Retry: false,
	}, nil
}
