package rootd

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// workloadPort identifies an intercepted container port of a workload.
type workloadPort struct {
	namespace string
	workload  string
	proto     types.Proto
	port      uint16
}

// shortcutTable maps cluster-side destinations of the client's active intercepts to
// their local intercept handlers.
type shortcutTable struct {
	// byAddr maps explicitly declared addresses, typically service ClusterIP:port.
	byAddr map[types.AddrPortProto]netip.AddrPort

	// byWorkload maps intercepted container ports, matched against the pod IPs of the
	// workload's traffic-agent pods.
	byWorkload map[workloadPort]netip.AddrPort
}

// validAddrPort unmarshals a netip.AddrPort from its binary form and requires the
// address to be valid.
func validAddrPort(b []byte) (ap netip.AddrPort, err error) {
	if err = ap.UnmarshalBinary(b); err == nil && !ap.IsValid() {
		err = fmt.Errorf("address %s is not valid", ap)
	}
	return ap, err
}

// newShortcutTable builds a shortcutTable from the given shortcuts. Invalid entries
// are skipped with a warning rather than rejecting the whole set, so that a newer
// user daemon doesn't lose the feature entirely when this daemon doesn't understand
// one of its entries.
func newShortcutTable(ctx context.Context, shortcuts []*rpc.InterceptShortcut) *shortcutTable {
	if len(shortcuts) == 0 {
		return nil
	}
	t := &shortcutTable{
		byAddr:     make(map[types.AddrPortProto]netip.AddrPort),
		byWorkload: make(map[workloadPort]netip.AddrPort),
	}
	for _, sc := range shortcuts {
		proto, err := types.ParseProto(sc.Protocol)
		if err == nil {
			var target netip.AddrPort
			if target, err = validAddrPort(sc.Target); err == nil {
				for _, sab := range sc.ServiceAddrs {
					var sa netip.AddrPort
					if sa, err = validAddrPort(sab); err != nil {
						break
					}
					t.byAddr[types.AddrPortProto{AddrPort: sa, Proto: proto}] = target
				}
				if err == nil && sc.Workload != "" && sc.ContainerPort > 0 {
					t.byWorkload[workloadPort{
						namespace: sc.Namespace,
						workload:  sc.Workload,
						proto:     proto,
						port:      uint16(sc.ContainerPort),
					}] = target
				}
			}
		}
		if err != nil {
			clog.Warnf(ctx, "ignoring invalid intercept shortcut for workload %s.%s: %v", sc.Workload, sc.Namespace, err)
		}
	}
	return t
}

// target returns the local intercept handler for the given destination. The podWorkload
// function resolves a pod IP to the workload that its traffic-agent belongs to.
func (t *shortcutTable) target(p types.Proto, dst netip.AddrPort, podWorkload func(netip.Addr) (workload, namespace string, ok bool)) (netip.AddrPort, bool) {
	if t == nil {
		return netip.AddrPort{}, false
	}
	if lt, ok := t.byAddr[types.AddrPortProto{AddrPort: dst, Proto: p}]; ok {
		return lt, true
	}
	if len(t.byWorkload) > 0 && podWorkload != nil {
		if wl, ns, ok := podWorkload(dst.Addr()); ok {
			if lt, ok := t.byWorkload[workloadPort{namespace: ns, workload: wl, proto: p, port: dst.Port()}]; ok {
				return lt, true
			}
		}
	}
	return netip.AddrPort{}, false
}

// SetInterceptShortcuts replaces the set of destinations that are connected directly
// to local intercept handlers instead of being tunneled to the cluster.
func (s *session) SetInterceptShortcuts(ctx context.Context, shortcuts []*rpc.InterceptShortcut) {
	s.interceptShortcuts.Store(newShortcutTable(ctx, shortcuts))
}

// shortcutTarget returns the local intercept handler for the given destination when
// one of the client's active intercepts covers it.
func (s *session) shortcutTarget(p types.Proto, dst netip.AddrPort) (netip.AddrPort, bool) {
	t := s.interceptShortcuts.Load()
	if t == nil {
		return netip.AddrPort{}, false
	}
	var podWorkload func(netip.Addr) (string, string, bool)
	if s.agentClients != nil {
		podWorkload = s.agentClients.WorkloadForIP
	}
	return t.target(p, dst, podWorkload)
}
