package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type aggIsolator struct {
	snapshots  chan string
	watches    chan []k8s.Resource
	aggregator *aggregator
	sup        *supervisor.Supervisor
	done       chan struct{}
	t          *testing.T
	cancel     context.CancelFunc
}

func newAggIsolator(t *testing.T) *aggIsolator {
	// aggregator uses zero length channels for its inputs so we can
	// control the total ordering of all inputs and therefore
	// intentionally trigger any order of events we want to test
	iso := &aggIsolator{
		// we need to create buffered channels for outputs
		// because nothing is asynchronously reading them in
		// the test
		watches:   make(chan []k8s.Resource, 100),
		snapshots: make(chan string, 100),
		// for signaling when the isolator is done
		done: make(chan struct{}),
	}
	iso.aggregator = NewAggregator(iso.snapshots, iso.watches, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	iso.cancel = cancel
	iso.sup = supervisor.WithContext(ctx)
	iso.sup.Supervise(&supervisor.Worker{
		Name: "aggregator",
		Work: iso.aggregator.Work,
	})
	return iso
}

func startAggIsolator(t *testing.T) *aggIsolator {
	iso := newAggIsolator(t)
	iso.Start()
	return iso
}

func (iso *aggIsolator) Start() {
	go func() {
		errs := iso.sup.Run()
		if len(errs) > 0 {
			iso.t.Errorf("unexpected errors: %v", errs)
		}
		close(iso.done)
	}()
}

func (iso *aggIsolator) Stop() {
	iso.sup.Shutdown()
	iso.cancel()
	<-iso.done
}

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

func TestAggregatorBug1(t *testing.T) {
	iso := startAggIsolator(t)
	defer iso.Stop()

	// initial kubernetes state is just services
	iso.aggregator.KubernetesEvents <- k8sEvent{"service", SERVICES}
	// we expect aggregator to generate a snapshot after the first event
	expect(t, iso.snapshots, func(value string) bool {
		return strings.Contains(value, "snapshot")
	})
	// whenever the aggregator sees updated k8s state, it
	// should send an update to the consul watch manager,
	// in this case it will be empty
	expect(t, iso.watches, []k8s.Resource(nil))

	iso.aggregator.KubernetesEvents <- k8sEvent{"configmap", RESOLVER}
	expect(t, iso.watches, func(watches []k8s.Resource) bool {
		if len(watches) != 1 {
			return false
		}

		if watches[0].Name() != "bar" {
			return false
		}

		return true
	})
}
