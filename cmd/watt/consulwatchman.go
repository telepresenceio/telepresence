package main

import (
	"fmt"
	"log"
	"os"

	"github.com/datawire/teleproxy/pkg/consulwatch"

	"github.com/datawire/teleproxy/pkg/supervisor"
	consulapi "github.com/hashicorp/consul/api"
)

type consulwatchman struct {
	WatchMaker                WatchMaker
	watchesCh                 <-chan []ConsulWatch
	consulEndpointsAggregator chan<- consulwatch.Endpoints
	watched                   map[string]*supervisor.Worker
}

type WatchMaker interface {
	MakeWatch(cw ConsulWatch, aggregatorCh chan<- consulwatch.Endpoints) (*supervisor.Worker, error)
}

type ConsulWatchMaker struct{}

func (m *ConsulWatchMaker) MakeWatch(cw ConsulWatch, aggregatorCh chan<- consulwatch.Endpoints) (*supervisor.Worker, error) {
	//return &supervisor.Worker{
	//	Name: "Foo",
	//	Work: func(process *supervisor.Process) error {
	//		fmt.Println("foobar")
	//		return nil
	//	},
	//}, nil

	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = cw.ConsulAddress

	// TODO: Should we really allocated a Consul client per Service watch? Not sure... there some design stuff here
	// May be multiple consul clusters
	// May be different connection parameters on the consulConfig
	// Seems excessive...
	consul, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, err
	}

	worker := &supervisor.Worker{
		Name: fmt.Sprintf("%s|%s|%s", cw.ConsulAddress, cw.Datacenter, cw.ServiceName),
		Work: func(p *supervisor.Process) error {
			w, err := consulwatch.New(consul, log.New(os.Stdout, "", log.LstdFlags), cw.Datacenter, cw.ServiceName, true)
			if err != nil {
				p.Logf("failed to setup new consul watch %v", err)
				return err
			}

			w.Watch(func(endpoints consulwatch.Endpoints, e error) { aggregatorCh <- endpoints })
			_ = p.Go(func(p *supervisor.Process) error {
				x := w.Start()
				if x != nil {
					p.Logf("failed to start service watcher %v", x)
					return x
				}

				return nil
			})

			<-p.Shutdown()
			w.Stop()
			return nil
		},
		Retry: true,
	}

	return worker, nil
}

func (w *consulwatchman) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		select {
		case watches := <-w.watchesCh:
			found := make(map[string]*supervisor.Worker)
			p.Logf("processing %d consul watches", len(watches))
			for _, cw := range watches {
				worker, err := w.WatchMaker.MakeWatch(cw, w.consulEndpointsAggregator)
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
			for workerName, worker := range w.watched {
				if _, exists := found[workerName]; !exists {
					p.Logf("remove consul watcher %s\n", workerName)
					worker.Shutdown()
					worker.Wait()
				}
			}

			w.watched = found
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}
