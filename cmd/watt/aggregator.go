package main

import (
	"encoding/json"
	"strings"

	"github.com/datawire/teleproxy/pkg/consulwatch"

	"github.com/datawire/teleproxy/pkg/watt"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type aggregator struct {
	// Input channel used to tell us about kubernetes state.
	KubernetesEvents chan k8sEvent
	// Input channel used to tell us about consul endpoints.
	ConsulEndpoints chan consulwatch.Endpoints
	// Output channel used to communicate with the consul watch manager.
	watches chan<- []k8s.Resource
	// Output channel used to communicate with the invoker.
	snapshots chan<- string
	// We won't consider ourselves "bootstrapped" until we hear
	// about all these kinds.
	requiredKinds       []string
	kubernetesResources map[string][]k8s.Resource
	consulEndpoints     map[string]consulwatch.Endpoints
	bootstrapped        bool
}

func NewAggregator(snapshots chan<- string, watches chan<- []k8s.Resource, requiredKinds []string) *aggregator {
	return &aggregator{
		KubernetesEvents:    make(chan k8sEvent),
		ConsulEndpoints:     make(chan consulwatch.Endpoints),
		watches:             watches,
		snapshots:           snapshots,
		requiredKinds:       requiredKinds,
		kubernetesResources: make(map[string][]k8s.Resource),
		consulEndpoints:     make(map[string]consulwatch.Endpoints),
	}
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		a.maybeNotify(p)
		select {
		case event := <-a.KubernetesEvents:
			a.setKubernetesResources(event)
			watches := a.kubernetesResources["consulresolver"]
			a.watches <- watches
			a.maybeNotify(p)
		case endpoints := <-a.ConsulEndpoints:
			a.updateConsulEndpoints(endpoints)
			a.maybeNotify(p)
		case <-p.Shutdown():
			return nil
		}
	}
}

func (a *aggregator) isKubernetesBootstrapped(p *supervisor.Process) bool {
	for _, k := range a.requiredKinds {
		_, ok := a.kubernetesResources[k]
		if !ok {
			return false
		}
	}
	return true
}

// Returns true if the current state of the world is complete. The
// kubernetes state of the world is always complete by definition
// because the kubernetes client provides that guarantee. The
// aggregate state of the world is complete when any consul services
// referenced by kubernetes have populated endpoint information (even
// if the value of the populated info is an empty set of endpoints).
func (a *aggregator) isComplete(p *supervisor.Process) bool {
	var requiredConsulServices []string

	for _, v := range a.kubernetesResources["consulresolver"] {
		// this is all kinds of type unsafe most likely
		requiredConsulServices = append(requiredConsulServices, v.Data()["service"].(string))
	}

	complete := true
	for _, name := range requiredConsulServices {
		_, ok := a.consulEndpoints[name]
		if !ok {
			p.Logf("waiting for endpoint info for %s", name)
			complete = false
		}
	}

	return complete
}

func (a *aggregator) maybeNotify(p *supervisor.Process) {
	if !a.isKubernetesBootstrapped(p) {
		return
	}

	if !a.bootstrapped && a.isComplete(p) {
		p.Logf("bootstrapped!")
		a.bootstrapped = true
	}

	if a.bootstrapped {
		snapshot, err := a.generateSnapshot()
		if err != nil {
			p.Logf("generate snapshot failed %v", err)
			return
		}

		a.snapshots <- snapshot
	}
}

func (a *aggregator) updateConsulEndpoints(endpoints consulwatch.Endpoints) {
	a.consulEndpoints[endpoints.Service] = endpoints
}

func (a *aggregator) setKubernetesResources(event k8sEvent) {
	a.kubernetesResources[event.kind] = event.resources
	if strings.HasPrefix(strings.ToLower(event.kind), "configmap") {
		resolvers := make([]k8s.Resource, 0)
		for _, r := range event.resources {
			if isConsulResolver(r) {
				resolvers = append(resolvers, r)
			}
		}
		a.kubernetesResources["consulresolver"] = resolvers
	}
}

func isConsulResolver(r k8s.Resource) bool {
	kind := strings.ToLower(r.Kind())
	if kind == "configmap" {
		a := r.Metadata().Annotations()
		if _, ok := a["getambassador.io/consul-resolver"]; ok {
			return true
		}
	}

	return false
}

func (a *aggregator) generateSnapshot() (string, error) {
	s := watt.Snapshot{
		Consul:     watt.ConsulSnapshot{Endpoints: a.consulEndpoints},
		Kubernetes: a.kubernetesResources,
	}

	jsonBytes, err := json.MarshalIndent(s, "", "    ")
	if err != nil {
		return "{}", err
	}

	return string(jsonBytes), nil
}
