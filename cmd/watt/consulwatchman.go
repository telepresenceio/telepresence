package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/davecgh/go-spew/spew"
	consulapi "github.com/hashicorp/consul/api"
)

type consulwatchman struct {
	WatchMaker                WatchMaker
	watchesCh                 <-chan []k8s.Resource
	consulEndpointsAggregator chan<- consulwatch.Endpoints
	watched                   map[string]*supervisor.Worker
}

type WatchMaker interface {
	MakeWatch(r k8s.Resource, aggregatorCh chan<- consulwatch.Endpoints) (*supervisor.Worker, error)
}

type ConsulWatchMaker struct{}

func (m *ConsulWatchMaker) MakeWatch(r k8s.Resource, aggregatorCh chan<- consulwatch.Endpoints) (*supervisor.Worker, error) {
	//return &supervisor.Worker{
	//	Name: "Foo",
	//	Work: func(process *supervisor.Process) error {
	//		fmt.Println("foobar")
	//		return nil
	//	},
	//}, nil

	// TODO: This code will need to be updated once we move to a CRD. The Data() method only works for ConfigMaps.
	data := r.Data()

	consulAddress, ok := data["consulAddress"].(string)
	if !ok {
		return nil, errors.New("failed to cast consulAddress as string")
	}

	consulAddress = strings.ToLower(consulAddress)
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = consulAddress

	// TODO: Should we really allocated a Consul client per Service watch? Not sure... there some design stuff here
	// May be multiple consul clusters
	// May be different connection parameters on the consulConfig
	// Seems excessive...
	consul, err := consulapi.NewClient(consulConfig)
	if err != nil {
		return nil, err
	}

	serviceName, ok := data["service"].(string)
	if !ok {
		return nil, errors.New("failed to cast service to a string")
	}

	datacenter, ok := data["datacenter"].(string)
	if !ok {
		return nil, errors.New("failed to cast datacenter to a string")
	}

	worker := &supervisor.Worker{
		Name: fmt.Sprintf("%s|%s|%s", consulAddress, datacenter, serviceName),
		Work: func(p *supervisor.Process) error {
			w, err := consulwatch.New(consul, log.New(os.Stdout, "", log.LstdFlags), datacenter, serviceName, true)
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

			//if worker != nil {
			//	// DO NOT remove unless you want warnings about not handling an error as Worker implements the
			//	// error interface.
			//}

			<-p.Shutdown()
			w.Stop()
			return nil
		},
		Retry: true,
	}

	//fmt.Println("===")
	//spew.Dump(worker)
	return worker, nil
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
					continue
					//panic(r)
				}
				worker, err := w.WatchMaker.MakeWatch(r, w.consulEndpointsAggregator)
				if err != nil {
					p.Logf("failed to create consul watch %v", err)
					continue
				}

				fmt.Println("===")
				spew.Dump(worker)
				fmt.Println("===")

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
					if err := worker.Wait(); err != nil {
						p.Logf("failed to remove consul watcher %s\n", workerName)
					}
				}
			}

			w.watched = found
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}
