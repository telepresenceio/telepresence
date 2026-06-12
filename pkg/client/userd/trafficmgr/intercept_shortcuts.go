package trafficmgr

import (
	"context"
	"net/netip"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// pushInterceptShortcuts declares the cluster-side destinations of the currently
// active intercepts to the root daemon, which connects them directly to their local
// intercept handlers instead of tunneling to the cluster. An empty set is pushed
// when the feature is disabled or no intercepts are active, since each push
// replaces the previous set.
func (s *session) pushInterceptShortcuts(intercepts []*manager.InterceptInfo) {
	var shortcuts []*daemon.InterceptShortcut
	if ic := client.GetConfig(s).Intercept(); ic.LocalShortcut {
		shortcuts = s.interceptShortcuts(intercepts, ic.LocalShortcutIsGlobal)
	}
	err := s.WithRootClient(s, func(ctx context.Context, rd daemon.DaemonClient) error {
		_, err := rd.SetInterceptShortcuts(ctx, &daemon.SetInterceptShortcutsRequest{Shortcuts: shortcuts})
		return err
	})
	switch status.Code(err) {
	case codes.OK:
	case codes.Unimplemented:
		// The root daemon predates intercept shortcuts.
	case codes.FailedPrecondition, codes.Canceled:
		// The root daemon is reconnecting or the session is ending. The next
		// snapshot push repairs the state.
		clog.Debugf(s, "unable to set intercept shortcuts: %v", err)
	default:
		clog.Warnf(s, "failed to set intercept shortcuts: %v", err)
	}
}

// shortcutEligible returns true when the given spec describes an intercept whose
// cluster-side destinations can be served by the local intercept handler. Filtered
// intercepts only receive matching requests, so they qualify only when isGlobal is
// set, on the assumption that the filters exist to limit how the intercept impacts
// others, not the developer's own traffic.
func shortcutEligible(spec *manager.InterceptSpec, isGlobal bool) bool {
	if isGlobal {
		return true
	}
	return len(spec.HeaderFilters) == 0 && len(spec.PathFilters) == 0 && (spec.Mechanism == "" || spec.Mechanism == "tcp")
}

// interceptShortcuts derives the shortcut declarations from the active intercepts of
// the given snapshot.
func (s *session) interceptShortcuts(intercepts []*manager.InterceptInfo, isGlobal bool) []*daemon.InterceptShortcut {
	shortcuts := make([]*daemon.InterceptShortcut, 0, len(intercepts))
	for _, ii := range intercepts {
		if ii.Disposition != manager.InterceptDispositionType_ACTIVE {
			continue
		}
		spec := ii.Spec
		if !shortcutEligible(spec, isGlobal) {
			continue
		}
		addr, err := netip.ParseAddr(spec.TargetHost)
		if err != nil {
			continue
		}
		// The target host may be a synthetic IP representing a hostname.
		if addr, err = s.Resolve(addr); err != nil {
			clog.Debugf(s, "no intercept shortcut for %s: %v", spec.Name, err)
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
		shortcuts = append(shortcuts, &daemon.InterceptShortcut{
			Namespace:     spec.Namespace,
			Workload:      spec.Agent,
			Protocol:      proto,
			ServiceAddrs:  shortcutServiceAddrs(spec),
			ContainerPort: uint32(spec.ContainerPort),
			Target:        target,
		})
	}
	return shortcuts
}

// shortcutServiceAddrs returns the ClusterIP:servicePort addresses of the service
// that the given intercept is made on, each in binary form. The cluster IPs are
// resolved by the traffic-manager when the intercept is prepared.
func shortcutServiceAddrs(spec *manager.InterceptSpec) (addrs [][]byte) {
	if spec.ServicePort == 0 {
		return nil
	}
	for _, ipb := range spec.ServiceIps {
		ip, ok := netip.AddrFromSlice(ipb)
		if !ok {
			continue
		}
		if ab, err := netip.AddrPortFrom(ip, uint16(spec.ServicePort)).MarshalBinary(); err == nil {
			addrs = append(addrs, ab)
		}
	}
	return addrs
}
