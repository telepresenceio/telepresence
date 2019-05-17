package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"

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
	ids                 map[string]bool
	kubernetesResources map[string]map[string][]k8s.Resource
	consulEndpoints     map[string]consulwatch.Endpoints
	bootstrapped        bool
	notifyMux           sync.Mutex
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
		kubernetesResources: make(map[string]map[string][]k8s.Resource),
		consulEndpoints:     make(map[string]consulwatch.Endpoints),
	}
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		select {
		case event := <-a.KubernetesEvents:
			a.setKubernetesResources(event)
			a.maybeNotify(p)
		case event := <-a.ConsulEvents:
			a.updateConsulResources(event)
			a.maybeNotify(p)
		case <-p.Shutdown():
			return nil
		}
	}
}

func (a *aggregator) updateConsulResources(event consulEvent) {
	a.ids[event.WatchId] = true
	a.consulEndpoints[event.Endpoints.Service] = event.Endpoints
}

func (a *aggregator) setKubernetesResources(event k8sEvent) {
	a.ids[event.watchId] = true
	submap, ok := a.kubernetesResources[event.watchId]
	if !ok {
		submap = make(map[string][]k8s.Resource)
		a.kubernetesResources[event.watchId] = submap
	}
	submap[event.kind] = event.resources
}

func (a *aggregator) generateSnapshot() (string, error) {
	k8sResources := make(map[string][]k8s.Resource)
	for _, submap := range a.kubernetesResources {
		for k, v := range submap {
			k8sResources[k] = append(k8sResources[k], v...)
		}
	}
	s := watt.Snapshot{
		Consul:     watt.ConsulSnapshot{Endpoints: a.consulEndpoints},
		Kubernetes: k8sResources,
	}

	jsonBytes, err := json.MarshalIndent(s, "", "    ")
	if err != nil {
		return "{}", err
	}

	return string(jsonBytes), nil
}

func (a *aggregator) isKubernetesBootstrapped(p *supervisor.Process) bool {
	submap, sok := a.kubernetesResources[""]
	if !sok {
		return false
	}
	for _, k := range a.requiredKinds {
		_, ok := submap[k]
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
		if _, ok := a.ids[w.WatchId()]; ok {
			p.Logf("initialized k8s watch: %s", w.WatchId())
		} else {
			complete = false
			p.Logf("waiting for k8s watch: %s", w.WatchId())
		}
	}

	for _, w := range watchset.ConsulWatches {
		if _, ok := a.ids[w.WatchId()]; ok {
			p.Logf("initialized k8s watch: %s", w.WatchId())
		} else {
			complete = false
			p.Logf("waiting for consul watch: %s", w.WatchId())
		}
	}

	return complete
}

func (a *aggregator) maybeNotify(p *supervisor.Process) {
	now := time.Now()
	delay := a.limiter.Limit(now)
	if delay == 0 {
		a.notify(p)
	} else if delay > 0 {
		time.AfterFunc(delay, func() {
			a.notify(p)
		})
	}
}

func (a *aggregator) notify(p *supervisor.Process) {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()

	if !a.isKubernetesBootstrapped(p) {
		return
	}

	watchset := a.getWatches(p)

	p.Logf("found %d kubernetes watches", len(watchset.KubernetesWatches))
	p.Logf("found %d consul watches", len(watchset.ConsulWatches))
	a.k8sWatches <- watchset.KubernetesWatches
	a.consulWatches <- watchset.ConsulWatches

	if !a.bootstrapped && a.isComplete(p, watchset) {
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
	return result.interpolate()
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
