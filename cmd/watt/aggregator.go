package main

import (
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/datawire/teleproxy/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/limiter"
	"github.com/datawire/teleproxy/pkg/watt"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type WatchHook func(p *supervisor.Process, snapshot string) WatchSet

type aggregator struct {
	// Input channel used to tell us about kubernetes state.
	KubernetesEvents chan k8sEvent
	// Input channel used to tell us about consul endpoints.
	ConsulEvents chan consulEvent
	// Output channel used to communicate with the k8s watch manager.
	k8sWatches chan<- []KubernetesWatchSpec
	// Output channel used to communicate with the consul watch manager.
	consulWatches chan<- []ConsulWatchSpec
	// Output channel used to communicate with the invoker.
	snapshots chan<- string
	// We won't consider ourselves "bootstrapped" until we hear
	// about all these kinds.
	requiredKinds       []string
	watchHook           WatchHook
	limiter             limiter.Limiter
	watchset            WatchSet
	ids                 map[string]bool
	kubernetesResources map[string][]k8s.Resource
	consulEndpoints     map[string]consulwatch.Endpoints
	bootstrapped        bool
}

func NewAggregator(snapshots chan<- string, k8sWatches chan<- []KubernetesWatchSpec, consulWatches chan<- []ConsulWatchSpec,
	requiredKinds []string, watchHook WatchHook, limiter limiter.Limiter) *aggregator {
	return &aggregator{
		KubernetesEvents:    make(chan k8sEvent),
		ConsulEvents:        make(chan consulEvent),
		k8sWatches:          k8sWatches,
		consulWatches:       consulWatches,
		snapshots:           snapshots,
		requiredKinds:       requiredKinds,
		watchHook:           watchHook,
		limiter:             limiter,
		ids:                 make(map[string]bool),
		kubernetesResources: make(map[string][]k8s.Resource),
		consulEndpoints:     make(map[string]consulwatch.Endpoints),
	}
}

func ExecWatchHook(watchHooks []string) WatchHook {
	return func(p *supervisor.Process, snapshot string) WatchSet {
		result := WatchSet{}

		for _, hook := range watchHooks {
			ws := invokeHook(p, hook, snapshot)
			result.KubernetesWatches = append(result.KubernetesWatches, ws.KubernetesWatches...)
			result.ConsulWatches = append(result.ConsulWatches, ws.ConsulWatches...)
		}

		return result
	}
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		a.maybeNotify(p)
		select {
		case event := <-a.KubernetesEvents:
			a.setKubernetesResources(event)
			a.updateWatches(p)
			a.maybeNotify(p)
		case event := <-a.ConsulEvents:
			a.updateConsulResources(event)
			a.updateWatches(p)
			a.maybeNotify(p)
		case <-p.Shutdown():
			return nil
		}
	}
}

func (a *aggregator) updateWatches(p *supervisor.Process) {
	a.watchset = a.getWatches(p)
	p.Log(a.watchset)

	p.Logf("found %d kubernetes watches", len(a.watchset.KubernetesWatches))
	p.Logf("found %d consul watches", len(a.watchset.ConsulWatches))
	a.k8sWatches <- a.watchset.KubernetesWatches
	a.consulWatches <- a.watchset.ConsulWatches
}

func (a *aggregator) updateConsulResources(event consulEvent) {
	a.ids[event.Id] = true
	a.consulEndpoints[event.Endpoints.Service] = event.Endpoints
}

func (a *aggregator) setKubernetesResources(event k8sEvent) {
	a.ids[event.id] = true
	a.kubernetesResources[event.kind] = event.resources
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
func (a *aggregator) isComplete(p *supervisor.Process, watchset WatchSet) bool {
	complete := true

	for _, w := range watchset.KubernetesWatches {
		if _, ok := a.ids[w.Id]; !ok {
			complete = false
			p.Logf("waiting for k8s watch: %s", w.Id)
		}
	}

	for _, w := range watchset.ConsulWatches {
		if _, ok := a.ids[w.Id]; !ok {
			complete = false
			p.Logf("waiting for consul watch: %s", w.Id)
		}
	}

	return complete
}

func (a *aggregator) maybeNotify(p *supervisor.Process) {
	if !a.isKubernetesBootstrapped(p) {
		return
	}

	if !a.bootstrapped && a.isComplete(p, a.watchset) {
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

func (a *aggregator) getWatches(p *supervisor.Process) WatchSet {
	snapshot, err := a.generateSnapshot()
	if err != nil {
		p.Logf("generate snapshot failed %v", err)
		return WatchSet{}
	}

	result := a.watchHook(p, snapshot)

	for idx, w := range result.KubernetesWatches {
		result.KubernetesWatches[idx].Id = w.Hash()
	}

	for idx, w := range result.ConsulWatches {
		result.ConsulWatches[idx].Id = w.Hash()
	}

	return result
}

func lines(st string) []string {
	return strings.Split(st, "\n")
}

func invokeHook(p *supervisor.Process, hook, snapshot string) WatchSet {
	cmd := exec.Command(hook)
	cmd.Stdin = strings.NewReader(snapshot)
	var watches, errors strings.Builder
	cmd.Stdout = &watches
	cmd.Stderr = &errors
	err := cmd.Run()
	stderr := errors.String()
	if stderr != "" {
		for _, line := range lines(stderr) {
			p.Logf("watch hook stderr: %s", line)
		}
	}
	if err != nil {
		p.Logf("watch hook failed: %v", err)
		return WatchSet{}
	}

	encoded := watches.String()

	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	result := WatchSet{}
	err = decoder.Decode(&result)
	if err != nil {
		for _, line := range lines(encoded) {
			p.Logf("watch hook: %s", line)
		}
		p.Logf("watchset decode failed: %v", err)
		return WatchSet{}
	}

	return result
}
