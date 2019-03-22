package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

// Bugs:
//
//  0. shutdown happens prior to bootstrap state being achieved
//
//  1. start up against an empty cluster
//     + we end up never achieving a bootstrapped state
//
//  2. start up against a changing cluster
//     + we learn about an initial set of consul services
//     + one of them gets deleted before its watcher gets an answer for us
//     + we will always have a nil entry in the required services map

func resources(input string) []k8s.Resource {
	result, err := k8s.ParseResources("aggregator-test", input)
	if err != nil {
		panic(err)
	}
	return result
}

var (
	SERVICES = resources(`
---
kind: Service
apiVersion: v1
metadata:
  name: foo
spec:
  selector:
    pod: foo
  ports:
  - protocol: TCP
    port: 80
    targetPort: 80
`)
	RESOLVER = resources(`
---
kind: ConfigMap
apiVersion: v1
metadata:
  name: bar
  annotations:
    "getambassador.io/consul-resolver": "true"
data:
  consulAddress: "127.0.0.1:8500"
  datacenter: "dc1"
  service: "bar"
`)
)

func TestAggregator(t *testing.T) {
	// by using zero length channels for inputs here, we can
	// control the total ordering of all inputs and therefore
	// intentionally trigger any order of events we want to test
	kubewatchesToAggregatorCh := make(chan k8sEvent)
	consulwatchesToAggregatorCh := make(chan consulwatch.Endpoints)
	// we need to create buffered channels for outputs because
	// nothing is asynchronously reading them in the test
	aggregatorToConsulwatchmanCh := make(chan []k8s.Resource, 100)
	aggregatorToInvokerCh := make(chan string, 100)

	agg := &aggregator{
		kubernetesEventsCh: kubewatchesToAggregatorCh,
		consulEndpointsCh:  consulwatchesToAggregatorCh,
		consulWatchesCh:    aggregatorToConsulwatchmanCh,
		snapshotCh:         aggregatorToInvokerCh,
		// XXX: the stuff below is really initializing
		// internal data structures of the aggregator, we
		// should probably have a constructor or something so
		// this kind of stuff doesn't need to appear in the
		// tests
		kubernetesResources: make(map[string][]k8s.Resource),
		consulEndpoints:     make(map[string]consulwatch.Endpoints),
	}

	sup := supervisor.WithContext(context.Background())

	go func() {
		timeout := time.NewTimer(1 * time.Second)

		// initial kubernetes state is just services
		kubewatchesToAggregatorCh <- k8sEvent{"service", SERVICES}

		// now we should see some watches
		// we expect aggregator to generate a snapshot after the first event
		select {
		case snapshot := <-aggregatorToInvokerCh:
			fmt.Println(snapshot)
		case <-timeout.C:
			t.Errorf("expecting to see a snapshot")
		}

		// whenever the aggregator sees updated k8s state, it
		// should send an update to the consul watch manager,
		// in this case it will be empty
		select {
		case watches := <-aggregatorToConsulwatchmanCh:
			fmt.Println(watches)
		case <-timeout.C:
			t.Errorf("expecting to see an empty watch update")
		}

		kubewatchesToAggregatorCh <- k8sEvent{"configmap", RESOLVER}

		select {
		case watches := <-aggregatorToConsulwatchmanCh:
			fmt.Println(watches)
		case <-timeout.C:
			t.Errorf("expecting to see bar")
		}
		sup.Shutdown()
	}()

	sup.Supervise(&supervisor.Worker{
		Name: "aggregator",
		Work: agg.Work,
	})
	errs := sup.Run()
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}
