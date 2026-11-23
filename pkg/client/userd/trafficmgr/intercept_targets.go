package trafficmgr

import (
	"context"
	"maps"
	"net/netip"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// interceptTargetsPublisher keeps the root daemon's declared set equal to the latest one the
// snapshots produced. Publishes are serialized, so an older set never overwrites a newer one,
// and a failed publish is retried until it succeeds or the session ends.
type interceptTargetsPublisher struct {
	mu       sync.Mutex
	targets  []*daemon.InterceptTarget
	dirty    bool
	retrying bool
}

const (
	interceptTargetsRPCTimeout = 10 * time.Second
	interceptTargetsRetryMin   = 200 * time.Millisecond
	interceptTargetsRetryMax   = 5 * time.Second
)

// destinationSet is the set of local destinations that one intercept may deliver traffic to.
type destinationSet map[types.AddrPortProto]struct{}

// newDestinationSet returns the destinations that a local intercept request asked for: the
// target port and the local side of each additional pod port, all on the target host.
func newDestinationSet(targetHost string, targetPort int32, protocol string, podPorts []string) destinationSet {
	addr, err := netip.ParseAddr(targetHost)
	if err != nil {
		return nil
	}
	set := make(destinationSet, 1+len(podPorts))
	add := func(port int32, proto string) {
		pr, err := types.ParseProto(proto)
		if err != nil || port <= 0 || port > 65535 {
			return
		}
		set[types.AddrPortProto{AddrPort: netip.AddrPortFrom(addr, uint16(port)), Proto: pr}] = struct{}{}
	}
	add(targetPort, protocol)
	for _, pms := range podPorts {
		to := types.PortMapping(pms).ToAsNumeric()
		add(int32(to.Port), to.Proto.String())
	}
	return set
}

// pushInterceptTargets declares the local destinations of the given intercepts to the root
// daemon, which dials a destination requested by a traffic-agent only if it is declared. Each
// push replaces the previous set. The set is in place when the call returns, unless the root
// daemon is unavailable, in which case it is retried in the background.
func (s *session) pushInterceptTargets(intercepts []*manager.InterceptInfo) {
	s.currentInterceptsLock.Lock()
	dests := maps.Clone(s.localDestinations)
	s.currentInterceptsLock.Unlock()
	p := &s.targetsPublisher
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets = interceptTargets(intercepts, dests)
	p.dirty = true
	s.flushInterceptTargetsLocked()
}

// resendInterceptTargets publishes the latest set again, as needed after the root daemon
// has been replaced.
func (s *session) resendInterceptTargets() {
	p := &s.targetsPublisher
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirty = true
	s.flushInterceptTargetsLocked()
}

// flushInterceptTargetsLocked must be called with the publisher's mutex held.
func (s *session) flushInterceptTargetsLocked() {
	p := &s.targetsPublisher
	if !p.dirty {
		return
	}
	ctx, cancel := context.WithTimeout(s, interceptTargetsRPCTimeout)
	defer cancel()
	err := s.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
		_, err := rd.SetInterceptTargets(ctx, &daemon.SetInterceptTargetsRequest{Targets: p.targets})
		return err
	})
	if err == nil {
		p.dirty = false
		return
	}
	switch status.Code(err) {
	case codes.FailedPrecondition, codes.Canceled:
		clog.Debugf(s, "unable to set intercept targets: %v", err)
	default:
		clog.Warnf(s, "failed to set intercept targets: %v", err)
	}
	if !p.retrying && s.Err() == nil {
		p.retrying = true
		go s.retryInterceptTargets()
	}
}

func (s *session) retryInterceptTargets() {
	p := &s.targetsPublisher
	backoff := interceptTargetsRetryMin
	for {
		select {
		case <-s.Done():
			return
		case <-time.After(backoff):
		}
		p.mu.Lock()
		s.flushInterceptTargetsLocked()
		done := !p.dirty
		if done {
			p.retrying = false
		}
		p.mu.Unlock()
		if done {
			return
		}
		backoff = min(backoff*2, interceptTargetsRetryMax)
	}
}

// interceptTargets returns the local destinations that the given intercepts deliver traffic to.
// A WAITING intercept is included because its traffic-agent may start forwarding before the
// intercept turns ACTIVE here. Only destinations that the local request for the intercept
// asked for are included; an intercept without such a record yields none. The target host may be a synthetic IP that stands in for a name,
// which is how the traffic-agent addresses it too.
func interceptTargets(intercepts []*manager.InterceptInfo, dests map[string]destinationSet) []*daemon.InterceptTarget {
	targets := make([]*daemon.InterceptTarget, 0, len(intercepts))
	for _, ii := range intercepts {
		if ii.Disposition != manager.InterceptDispositionType_ACTIVE && ii.Disposition != manager.InterceptDispositionType_WAITING {
			continue
		}
		spec := ii.Spec
		allowed := dests[spec.Name]
		if len(allowed) == 0 {
			continue
		}
		addr, err := netip.ParseAddr(spec.TargetHost)
		if err != nil {
			continue
		}
		type portAndProto struct {
			port  int32
			proto string
		}
		ports := make([]portAndProto, 0, 1+len(spec.PodPorts))
		ports = append(ports, portAndProto{spec.TargetPort, spec.Protocol})
		// Each additional pod port is delivered to a local port of its own, on the same host.
		for _, pms := range spec.PodPorts {
			to := types.PortMapping(pms).ToAsNumeric()
			ports = append(ports, portAndProto{int32(to.Port), to.Proto.String()})
		}
		type workload struct{ namespace, name string }
		workloads := []workload{{spec.Namespace, spec.Agent}}
		// A shared-service intercept receives traffic from every participating workload.
		for _, sw := range ii.ServiceWorkloads {
			if w := (workload{sw.Namespace, sw.WorkloadName}); w.name != "" && !slices.Contains(workloads, w) {
				workloads = append(workloads, w)
			}
		}
		for _, pp := range ports {
			if pp.port <= 0 || pp.port > 65535 {
				continue
			}
			ap := netip.AddrPortFrom(addr, uint16(pp.port))
			pr, err := types.ParseProto(pp.proto)
			if err != nil {
				continue
			}
			if _, ok := allowed[types.AddrPortProto{AddrPort: ap, Proto: pr}]; !ok {
				continue
			}
			target, err := ap.MarshalBinary()
			if err != nil {
				continue
			}
			proto := pr.String()
			for _, wl := range workloads {
				targets = append(targets, &daemon.InterceptTarget{
					Namespace: wl.namespace,
					Workload:  wl.name,
					Protocol:  proto,
					Target:    target,
				})
			}
		}
	}
	return targets
}
