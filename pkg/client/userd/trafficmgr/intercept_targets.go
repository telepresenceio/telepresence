package trafficmgr

import (
	"context"
	"net/netip"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// pushInterceptTargets declares the local destinations of the given intercepts to the root
// daemon, which dials a destination requested by a traffic-agent only if it is declared. Each
// push replaces the previous set.
func (s *session) pushInterceptTargets(intercepts []*manager.InterceptInfo) {
	targets := interceptTargets(intercepts)
	err := s.WithRootClient(s, func(ctx context.Context, rd daemon.DaemonClient) error {
		_, err := rd.SetInterceptTargets(ctx, &daemon.SetInterceptTargetsRequest{Targets: targets})
		return err
	})
	switch status.Code(err) {
	case codes.OK:
	case codes.FailedPrecondition, codes.Canceled:
		// The root daemon is reconnecting or the session is ending. The reconnect pushes the set again.
		clog.Debugf(s, "unable to set intercept targets: %v", err)
	default:
		clog.Warnf(s, "failed to set intercept targets: %v", err)
	}
}

// interceptTargets returns the local destinations that the given intercepts deliver traffic to.
// A WAITING intercept is included because its traffic-agent may start forwarding before the
// intercept turns ACTIVE here. The target host may be a synthetic IP that stands in for a name,
// which is how the traffic-agent addresses it too.
func interceptTargets(intercepts []*manager.InterceptInfo) []*daemon.InterceptTarget {
	targets := make([]*daemon.InterceptTarget, 0, len(intercepts))
	for _, ii := range intercepts {
		if ii.Disposition != manager.InterceptDispositionType_ACTIVE && ii.Disposition != manager.InterceptDispositionType_WAITING {
			continue
		}
		spec := ii.Spec
		addr, err := netip.ParseAddr(spec.TargetHost)
		if err != nil || spec.TargetPort <= 0 || spec.TargetPort > 65535 {
			continue
		}
		target, err := netip.AddrPortFrom(addr, uint16(spec.TargetPort)).MarshalBinary()
		if err != nil {
			continue
		}
		proto := spec.Protocol
		if proto == "" {
			proto = types.ProtoTCP.String()
		}
		targets = append(targets, &daemon.InterceptTarget{
			Namespace: spec.Namespace,
			Workload:  spec.Agent,
			Protocol:  proto,
			Target:    target,
		})
	}
	return targets
}
