package quicforwarder

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
)

// allowlistWatchRetryInterval is how often WatchAllowlist retries WatchQuicBackends
// after a failure, via pkg/grpc/watcher.WatchWithRetry's constant backoff.
const allowlistWatchRetryInterval = 5 * time.Second

// backendSet is one immutable, full-replacement snapshot of the live backend
// allowlist: every allowlisted pod IP's kind, plus the subset that are managers
// (precomputed so ManagerAddr doesn't rebuild it on every call).
type backendSet struct {
	kinds    map[netip.Addr]string
	managers []netip.Addr
}

// Allowlist is the forwarder's atomic view of the live manager/agent pod IPs a
// routing decision may validate against, per "Backend allowlist (required)" in "The
// forwarder" section of docs/plans/quic-transport/design.md. It is updated by
// WatchAllowlist and read (via the backendPicker interface) by Router.
//
// It implements backendPicker.
type Allowlist struct {
	snapshot  atomic.Pointer[backendSet]
	ready     atomic.Bool
	readyOnce sync.Once
}

// NewAllowlist returns an Allowlist with no snapshot yet: Ready() is false and
// Contains/ManagerAddr report nothing is allowlisted until the first snapshot arrives.
func NewAllowlist() *Allowlist {
	return &Allowlist{}
}

// Ready implements backendPicker.
func (a *Allowlist) Ready() bool {
	return a.ready.Load()
}

// Contains implements backendPicker.
func (a *Allowlist) Contains(ip netip.Addr) bool {
	s := a.snapshot.Load()
	if s == nil {
		return false
	}
	_, ok := s.kinds[ip]
	return ok
}

// ManagerAddr implements backendPicker. Per the design, any allowlisted manager
// backend is an acceptable resolution for quicfwd.ManagerSNI; which one is returned
// when several are allowlisted is unspecified (Go's randomized map iteration order
// gives basic distribution across replicas for free).
func (a *Allowlist) ManagerAddr() (netip.Addr, bool) {
	s := a.snapshot.Load()
	if s == nil || len(s.managers) == 0 {
		return netip.Addr{}, false
	}
	return s.managers[0], true
}

// update replaces the current snapshot with backends, and -- exactly once, on the
// first snapshot ever received -- logs that the allowlist is now ready. It never
// clears the snapshot on its own: losing the manager (WatchAllowlist's stream ending)
// keeps the last known-good snapshot, per the design's "soft state" treatment of the
// allowlist.
func (a *Allowlist) update(ctx context.Context, backends []*rpc.QuicBackend) {
	kinds := make(map[netip.Addr]string, len(backends))
	var managers []netip.Addr
	for _, b := range backends {
		ip, ok := netip.AddrFromSlice(b.Ip)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		kinds[ip] = b.Kind
		if b.Kind == "manager" {
			managers = append(managers, ip)
		}
	}
	a.snapshot.Store(&backendSet{kinds: kinds, managers: managers})
	a.readyOnce.Do(func() {
		a.ready.Store(true)
		clog.Infof(ctx, "quic-forwarder: received first backend allowlist snapshot (%d backend(s))", len(kinds))
	})
}

// WatchAllowlist dials the traffic-manager at address and keeps allowlist up to date
// from WatchQuicBackends, reconnecting with backoff on any error (pkg/grpc/watcher's
// constant-backoff retry, the same helper the traffic-agent uses for its own manager
// watches). It runs until ctx is done, at which point it returns nil.
//
// Losing the manager mid-run does not clear allowlist: WatchWithRetry simply keeps
// retrying, and allowlist.update is only ever called with a newer snapshot, never with
// an empty one on disconnect.
func WatchAllowlist(ctx context.Context, address string, allowlist *Allowlist) error {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	manager := rpc.NewManagerClient(conn)

	return watcher.WatchWithRetry(ctx, "WatchQuicBackends", allowlistWatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.QuicBackendSnapshot], error) {
			return manager.WatchQuicBackends(ctx, &empty.Empty{})
		},
		func(snap *rpc.QuicBackendSnapshot) error {
			allowlist.update(ctx, snap.Backends)
			return nil
		},
		nil,
	)
}
