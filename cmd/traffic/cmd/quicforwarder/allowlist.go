package quicforwarder

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
)

// allowlistWatchRetryInterval is how often WatchAllowlist retries WatchQuicBackends
// after a failure, via pkg/grpc/watcher.WatchWithRetry's constant backoff.
const allowlistWatchRetryInterval = 5 * time.Second

// backendInfo is one allowlisted backend's kind and QUIC port.
type backendInfo struct {
	kind string
	port uint16
}

// backendSet is one immutable, full-replacement snapshot of the live backend
// allowlist: every allowlisted pod IP's kind and port, plus the subsets needed to
// resolve an SNI without a linear scan -- the manager IPs (precomputed so
// ManagerBackend doesn't rebuild it on every call) and agent pod UID -> IP
// (precomputed so AgentBackend can resolve an AgentSNI's pod UID directly).
type backendSet struct {
	backends    map[netip.Addr]backendInfo
	managers    []netip.Addr
	agentsByUID map[string]netip.Addr
}

// Allowlist is the forwarder's atomic view of the live manager/agent pod IPs a
// routing decision may validate against, per "Backend allowlist (required)" in "The
// forwarder" section of docs/reference/quic-transport-architecture.md. It is updated by
// WatchAllowlist and read (via the backendPicker interface) by Router.
//
// It implements backendPicker.
type Allowlist struct {
	// fallbackManagerPort is used for a manager entry whose QuicBackend.Port is
	// 0 -- an older traffic-manager that doesn't yet report its own QUIC port.
	// See config.go's Env.BackendPort doc comment for the full rationale; this
	// keeps a rolling upgrade from an older manager (or a manager whose
	// WatchQuicBackends response predates this field) working without also
	// upgrading the forwarder in lockstep. It is never applied to agent
	// entries: an agent with no reported port has no QUIC listener at all, and
	// silently guessing one would risk routing traffic to an unrelated
	// listener on that pod.
	fallbackManagerPort uint16

	snapshot  atomic.Pointer[backendSet]
	ready     atomic.Bool
	readyOnce sync.Once
}

// NewAllowlist returns an Allowlist with no snapshot yet: Ready() is false and
// Backend/ManagerBackend/AgentBackend report nothing is allowlisted until the first
// snapshot arrives. fallbackManagerPort is used for a manager entry whose reported
// port is 0; see the Allowlist.fallbackManagerPort doc comment.
func NewAllowlist(fallbackManagerPort uint16) *Allowlist {
	return &Allowlist{fallbackManagerPort: fallbackManagerPort}
}

// Ready implements backendPicker.
func (a *Allowlist) Ready() bool {
	return a.ready.Load()
}

// Contains reports whether ip is a currently live, allowlisted backend (manager or
// agent). Test/diagnostic convenience; Router itself calls Backend, which also
// resolves the port a CID-routed datagram must be forwarded to.
func (a *Allowlist) Contains(ip netip.Addr) bool {
	_, ok := a.Backend(ip)
	return ok
}

// Backend implements backendPicker.
func (a *Allowlist) Backend(ip netip.Addr) (uint16, bool) {
	s := a.snapshot.Load()
	if s == nil {
		return 0, false
	}
	info, ok := s.backends[ip]
	return info.port, ok
}

// ManagerBackend implements backendPicker. Any allowlisted manager backend is an
// acceptable resolution for quicfwd.ManagerSNI; the first in the snapshot's order is
// returned. The traffic-manager runs as a single replica (more than one is unsupported
// for the QUIC path; see "Non-goals" in docs/reference/quic-transport-architecture.md),
// so several entries only occur transiently, during a manager rollout.
func (a *Allowlist) ManagerBackend() (netip.Addr, uint16, bool) {
	s := a.snapshot.Load()
	if s == nil || len(s.managers) == 0 {
		return netip.Addr{}, 0, false
	}
	ip := s.managers[0]
	return ip, s.backends[ip].port, true
}

// AgentBackend implements backendPicker.
func (a *Allowlist) AgentBackend(podUID string) (netip.Addr, uint16, bool) {
	s := a.snapshot.Load()
	if s == nil {
		return netip.Addr{}, 0, false
	}
	ip, ok := s.agentsByUID[podUID]
	if !ok {
		return netip.Addr{}, 0, false
	}
	return ip, s.backends[ip].port, true
}

// update replaces the current snapshot with backends, and -- exactly once, on the
// first snapshot ever received -- logs that the allowlist is now ready. It never
// clears the snapshot on its own: losing the manager (WatchAllowlist's stream ending)
// keeps the last known-good snapshot, per the design's "soft state" treatment of the
// allowlist.
//
// A backend whose resolved port is 0 (an agent entry with no reported port, or a
// manager entry with no reported port and no fallbackManagerPort configured) is
// dropped from the snapshot entirely: a port-0 "backend" cannot be dialed, and
// admitting it into the allowlist would only let a CID or SNI resolve to a dead end
// instead of falling through to dropUnresolvedSNI/dropAllowlistMiss.
func (a *Allowlist) update(ctx context.Context, backends []*rpc.QuicBackend) {
	infos := make(map[netip.Addr]backendInfo, len(backends))
	var managers []netip.Addr
	agentsByUID := make(map[string]netip.Addr)
	for _, b := range backends {
		ip, ok := netip.AddrFromSlice(b.Ip)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		port := uint16(b.Port)
		if port == 0 && b.Kind == "manager" {
			port = a.fallbackManagerPort
		}
		if port == 0 {
			continue
		}
		infos[ip] = backendInfo{kind: b.Kind, port: port}
		switch b.Kind {
		case "manager":
			managers = append(managers, ip)
		case "agent":
			if b.PodUid != "" {
				agentsByUID[b.PodUid] = ip
			}
		}
	}
	a.snapshot.Store(&backendSet{backends: infos, managers: managers, agentsByUID: agentsByUID})
	a.readyOnce.Do(func() {
		a.ready.Store(true)
		clog.Infof(ctx, "quic-forwarder: received first backend allowlist snapshot (%d backend(s))", len(infos))
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
	// Keepalives so a manager pod that vanishes (a rollout, a crash) is noticed within
	// seconds rather than whenever TCP eventually gives up: WatchWithRetry can only
	// resubscribe once the dead stream errors, and until it does the allowlist keeps
	// the departed pod's IP, so a manager-bound QUIC dial routes to a dead backend.
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}))
	if err != nil {
		return err
	}
	defer conn.Close()
	manager := rpc.NewManagerClient(conn)

	// retries counts repair invocations, i.e. attempts after the first. While
	// allowlist has never received a snapshot, this warns on the 3rd retry and
	// roughly once a minute thereafter (every 12th retry, at the 5 second
	// allowlistWatchRetryInterval), so a persistently unreachable manager is
	// visible instead of silently dropping every datagram. Once a snapshot has
	// been received, it never warns again: further retries are ordinary
	// reconnects already logged by the watcher.
	retries := 0
	repair := func() error {
		retries++
		if !allowlist.Ready() && retries%12 == 3 {
			clog.Warnf(ctx, "quic-forwarder: still no backend allowlist snapshot after %d attempts; all datagrams are dropped until one arrives", retries)
		}
		return nil
	}

	return watcher.WatchWithRetry(ctx, "WatchQuicBackends", allowlistWatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[rpc.QuicBackendSnapshot], error) {
			return manager.WatchQuicBackends(ctx, &empty.Empty{})
		},
		func(snap *rpc.QuicBackendSnapshot) error {
			allowlist.update(ctx, snap.Backends)
			return nil
		},
		repair,
	)
}
