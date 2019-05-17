package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/consulwatch"

	"github.com/datawire/teleproxy/pkg/watt"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/limiter"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type aggIsolator struct {
	snapshots     chan string
	k8sWatches    chan []KubernetesWatchSpec
	consulWatches chan []ConsulWatchSpec
	aggregator    *aggregator
	sup           *supervisor.Supervisor
	done          chan struct{}
	t             *testing.T
	cancel        context.CancelFunc
}

func newAggIsolator(t *testing.T, requiredKinds []string, watchHook WatchHook) *aggIsolator {
	// aggregator uses zero length channels for its inputs so we can
	// control the total ordering of all inputs and therefore
	// intentionally trigger any order of events we want to test
	iso := &aggIsolator{
		// we need to create buffered channels for outputs
		// because nothing is asynchronously reading them in
		// the test
		k8sWatches:    make(chan []KubernetesWatchSpec, 100),
		consulWatches: make(chan []ConsulWatchSpec, 100),
		snapshots:     make(chan string, 100),
		// for signaling when the isolator is done
		done: make(chan struct{}),
	}
	iso.aggregator = NewAggregator(iso.snapshots, iso.k8sWatches, iso.consulWatches, requiredKinds, watchHook,
		limiter.NewUnlimited())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	iso.cancel = cancel
	iso.sup = supervisor.WithContext(ctx)
	iso.sup.Supervise(&supervisor.Worker{
		Name: "aggregator",
		Work: iso.aggregator.Work,
	})
	iso.t = t
	return iso
}

func startAggIsolator(t *testing.T, requiredKinds []string, watchHook WatchHook) *aggIsolator {
	iso := newAggIsolator(t, requiredKinds, watchHook)
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

// make sure we shutdown even before achieving a bootstrapped state
func TestAggregatorShutdown(t *testing.T) {
	iso := startAggIsolator(t, nil, nil)
	defer iso.Stop()
}

var WATCH = ConsulWatchSpec{
	ConsulAddress: "127.0.0.1:8500",
	Datacenter:    "dc1",
	ServiceName:   "bar",
}

// Check that we bootstrap properly... this means *not* emitting a
// snapshot until we have:
//
//   a) achieved synchronization with the kubernetes API server
//
//   b) received (possibly empty) endpoint info about all referenced
//      consul services...
func TestAggregatorBootstrap(t *testing.T) {
	watchHook := func(p *supervisor.Process, snapshot string) WatchSet {
		if strings.Contains(snapshot, "configmap") {
			return WatchSet{
				ConsulWatches: []ConsulWatchSpec{WATCH},
			}
		} else {
			return WatchSet{}
		}
	}
	iso := startAggIsolator(t, []string{"service", "configmap"}, watchHook)
	defer iso.Stop()

	// initial kubernetes state is just services
	iso.aggregator.KubernetesEvents <- k8sEvent{"", "service", SERVICES}

	// we should not generate a snapshot or consulWatches yet
	// because we specified configmaps are required
	expect(t, iso.consulWatches, Timeout(100*time.Millisecond))
	expect(t, iso.snapshots, Timeout(100*time.Millisecond))

	// the configmap references a consul service, so we shouldn't
	// get a snapshot yet, but we should get watches
	iso.aggregator.KubernetesEvents <- k8sEvent{"", "configmap", RESOLVER}
	expect(t, iso.snapshots, Timeout(100*time.Millisecond))
	expect(t, iso.consulWatches, func(watches []ConsulWatchSpec) bool {
		if len(watches) != 1 {
			t.Logf("expected 1 watch, got %d watches", len(watches))
			return false
		}

		if watches[0].ServiceName != "bar" {
			return false
		}

		return true
	})

	// now lets send in the first endpoints, and we should get a
	// snapshot
	iso.aggregator.ConsulEvents <- consulEvent{
		WATCH.WatchId(),
		consulwatch.Endpoints{
			Service: "bar",
			Endpoints: []consulwatch.Endpoint{
				{
					Service: "bar",
					Address: "1.2.3.4",
					Port:    80,
				},
			},
		},
	}

	expect(t, iso.snapshots, func(snapshot string) bool {
		s := &watt.Snapshot{}
		err := json.Unmarshal([]byte(snapshot), s)
		if err != nil {
			return false
		}
		_, ok := s.Consul.Endpoints["bar"]
		return ok
	})
}
