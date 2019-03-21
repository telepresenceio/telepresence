package main

import (
	"fmt"
	"strings"

	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type aggregator struct {
	kubernetesEventsCh  <-chan k8sEvent
	kubernetesResources map[string][]k8s.Resource
	consulEndpointsCh   <-chan consulwatch.Endpoints
	consulEndpoints     map[string]consulwatch.Endpoints
	consulWatchesCh     chan<- []k8s.Resource
	snapshotCh          chan<- string
	bootstrapped        bool
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		a.maybeNotify(p)
		select {
		case event := <-a.kubernetesEventsCh:
			a.setKubernetesResources(event)
			watches := a.kubernetesResources["ConsulResolver"]
			a.consulWatchesCh <- watches
			a.maybeNotify(p)
		case endpoints := <-a.consulEndpointsCh:
			a.updateConsulEndpoints(endpoints)
			a.maybeNotify(p)
		}
	}
}

func (a *aggregator) isKubernetesBootstrapped(p *supervisor.Process) bool {
	// XXX: initialSources is a global
	return len(a.kubernetesResources) >= len(initialSources)
}

// Returns true if the current state of the world is complete. The
// kubernetes state of the world is always complete by definition
// because the kubernetes client provides that guarantee. The
// aggregate state of the world is complete when any consul services
// referenced by kubernetes have populated endpoint information (even
// if the value of the populated info is an empty set of endpoints).
func (a *aggregator) isComplete(p *supervisor.Process) bool {
	var requiredConsulServices []string

	for _, v := range a.kubernetesResources["ConsulResolver"] {
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
		a.snapshotCh <- a.generateSnapshot()
	}
}

func (a *aggregator) updateConsulEndpoints(endpoints consulwatch.Endpoints) {
	fmt.Println(endpoints)
	a.consulEndpoints[endpoints.Service] = endpoints
}

func (a *aggregator) setKubernetesResources(event k8sEvent) {
	a.kubernetesResources[event.kind] = event.resources
	if strings.ToLower(event.kind) == "configmap" {
		var resolvers []k8s.Resource
		for _, r := range event.resources {
			if isConsulResolver(r) {
				resolvers = append(resolvers, r)
			}
		}
		a.kubernetesResources["ConsulResolver"] = resolvers
	}
}

func (a *aggregator) generateSnapshot() string {
	return "generate snapshot stub\n"
}
