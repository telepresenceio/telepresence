package agentpf

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	tpClient "github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type client struct {
	// Mutex protects the following fields (the rest is immutable)
	//   info.intercepted
	//   cli
	//   cancelClient
	//   cancelDialWatch
	// cli and cancelClient are both safe to use without a mutex once the ready channel is closed.
	*k8s.Cluster
	sync.RWMutex
	cli             agent.AgentClient
	session         *manager.SessionInfo
	info            *manager.AgentPodInfo
	remove          func()
	owner           *clients
	cancelClient    context.CancelFunc
	cancelDialWatch context.CancelFunc
	dialWatchID     uint64
	tunnelCount     int32
	lastActive      int64

	// quicDead is set the first time a QUIC dial, TLS handshake, or stream-open attempt
	// fails for this agent pod. Once set, the dialer built in dialAgent goes straight to
	// the port-forward path for the remainder of this AgentPodInfo generation; refresh
	// clears it whenever the watch reports an updated AgentPodInfo for the pod, so a pod
	// restart gets a fresh chance.
	quicDead atomic.Bool

	// quicConnMu guards quicConn. It is a dedicated mutex, distinct from the RWMutex
	// above, because the dialer that reads and writes quicConn runs on a goroutine
	// managed by grpc-go's connection machinery and must never depend on (or block)
	// whatever holds the client's main lock.
	quicConnMu sync.Mutex
	// quicConn is this agent's cached QUIC connection, reused to open additional streams,
	// e.g. after grpc's own reconnect machinery redials following a stream failure.
	quicConn *quic.Conn

	// transport records which transport currently carries the live gRPC connection to
	// this agent ("quic" or "grpc"), read by Transports() for the status surface. Empty
	// until a connection attempt has completed at least once.
	transport atomic.Value
}

const (
	dormantLingerTime             = 5 * time.Second
	dialWatcherReconnectInitial   = 250 * time.Millisecond
	dialWatcherReconnectMax       = 5 * time.Second
	dialWatcherReconnectResetTime = 30 * time.Second
)

// connectRetryInterval is how long WaitForIP waits between attempts to reach an agent that the
// watch reports as present but that isn't dialable yet.
const connectRetryInterval = 200 * time.Millisecond

func (ac *client) String() string {
	if ac == nil {
		return "<nil>"
	}
	ai := ac.info
	return fmt.Sprintf("%s(%s), port %d", ai.PodName, net.IP(ai.PodIp), ai.ApiPort)
}

func (ac *client) Tunnel(ctx context.Context, opts ...grpc.CallOption) (tunnel.GRPCClientStream, error) {
	cli, err := ac.ensureConnect(ctx)
	if err != nil {
		return nil, err
	}
	clog.Tracef(ctx, "%s(%s) creating Tunnel over gRPC", ac, net.IP(ac.info.PodIp))
	tc, err := cli.Tunnel(ctx, opts...)
	if err != nil {
		clog.Tracef(ctx, "%s(%s) failed to create Tunnel over gRPC: %v", ac, net.IP(ac.info.PodIp), err)
		return nil, err
	}
	atomic.AddInt32(&ac.tunnelCount, 1)
	clog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), atomic.LoadInt32(&ac.tunnelCount))
	go func() {
		<-ctx.Done()
		tc := atomic.LoadInt32(&ac.tunnelCount)
		if tc > 0 && atomic.CompareAndSwapInt32(&ac.tunnelCount, tc, tc-1) {
			clog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), tc-1)
		}
	}()
	atomic.StoreInt64(&ac.lastActive, time.Now().UnixNano())
	return tc, nil
}

func (ac *client) ensureConnect(ctx context.Context) (agent.AgentClient, error) {
	ac.Lock()
	defer ac.Unlock()
	return ac.ensureConnectLocked(ctx)
}

func (ac *client) ensureConnectLocked(ctx context.Context) (agent.AgentClient, error) {
	if ac.info.Intercepted {
		ac.startDialWatcherLocked()
	}

	if ac.cli == nil {
		tos := tpClient.GetConfig(ac).Timeouts()
		dialCtx, dialCancel := tos.TimeoutContext(ctx, tpClient.TimeoutTrafficAgentConnect)
		defer dialCancel()

		ai := ac.info
		ns := ai.Namespace
		if ai.NodeAgent {
			ns = k8s.GetManagerNamespace(ctx)
		}
		conn, cli, err := ac.dialAgent(dialCtx, ns, ai)
		if err != nil {
			if ac.info.Intercepted {
				return nil, err
			}

			// There's a risk for deadlock here, because of the Range iteration of the map that performs cancel. This cancel will block
			// because we're holding the lock now, and since the Range iteration holds a lock for the entry that we're about to delete,
			// that delete will block. So we let a potential cancel call continue by unlocking before we delete.
			ac.Unlock()
			ac.remove()
			ac.Lock() // Must of course lock again to prevent panic by the pending unlock.
			return nil, err
		}

		ac.cli = cli
		ac.cancelClient = func() {
			// Need to run this in a separate thread to avoid deadlock.
			go func() {
				conn.Close()
				ac.closeQuicConn()
				ac.Lock()
				atomic.StoreInt32(&ac.tunnelCount, 0)
				ac.cancelClient = nil
				ac.cli = nil
				ac.transport.Store("")
				ac.Unlock()
			}()
		}
	}

	atomic.StoreInt64(&ac.lastActive, time.Now().UnixNano())
	return ac.cli, nil
}

// dialAgent establishes the gRPC connection to this agent's API port. When the agent
// advertises a quic_sni (ai.QuicSni != "") and QUIC hasn't previously failed for it, the
// dialer handed to grpc tries QUIC first for every connection attempt -- including the ones
// grpc's own reconnect machinery makes transparently after a transport failure -- and falls
// back to the Kubernetes port-forward otherwise.
func (ac *client) dialAgent(dialCtx context.Context, ns string, ai *manager.AgentPodInfo) (*grpc.ClientConn, agent.AgentClient, error) {
	podID := types.UID(ai.PodId)
	var grpcAddr string
	if podID == "" {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d", ai.PodName, ns, ai.ApiPort)
	} else {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d#%s", ai.PodName, ns, ai.ApiPort, podID)
	}

	pfDialer := portforward.Dialer(ac.Cluster)
	dialer := agentDialer(ai.QuicSni, &ac.quicDead, &ac.transport,
		ac.owner.quicEndpointFor, ac.dialAgentQUIC, pfDialer,
		func(err error) { clog.Infof(ac, "%s: QUIC dial failed, falling back to port-forward: %v", ac, err) })

	// tokenCredentials attaches the session token, if any, as gRPC metadata on every
	// call this connection makes -- both the QUIC and port-forward transports share
	// this one *grpc.ClientConn, so this single DialOption covers both.
	conn, err := grpcClient.DialGRPC(dialCtx, portforward.K8sPFScheme+":///"+grpcAddr,
		grpc.WithContextDialer(dialer),
		grpc.WithResolvers(portforward.NewResolver(ac.Cluster)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 24 * time.Hour, Timeout: 20 * time.Second}),
		grpc.WithIdleTimeout(0),
		grpc.WithPerRPCCredentials(tokenCredentials{provider: ac.owner.tokenProvider}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	cli := agent.NewAgentClient(conn)
	if _, err := cli.Version(dialCtx, &empty.Empty{}); err != nil {
		conn.Close()
		return nil, nil, tpClient.CheckTimeout(dialCtx, fmt.Errorf("dial agent: %w", err))
	}
	return conn, cli, nil
}

func (ac *client) idleTime() time.Duration {
	return time.Duration(time.Now().UnixNano() - atomic.LoadInt64(&ac.lastActive))
}

func (ac *client) dormant() bool {
	if atomic.LoadInt32(&ac.tunnelCount) > 0 || ac.idleTime() < dormantLingerTime {
		return false
	}
	ac.RLock()
	dormant := ac.cli != nil && !ac.info.Intercepted
	ac.RUnlock()
	return dormant
}

func (ac *client) connected() bool {
	ac.RLock()
	ok := ac.cli != nil
	ac.RUnlock()
	return ok
}

func (ac *client) intercepted() bool {
	ac.RLock()
	ret := ac.info.Intercepted
	ac.RUnlock()
	return ret
}

func (ac *client) cancel() bool {
	ac.RLock()
	cc := ac.cancelClient
	cdw := ac.cancelDialWatch
	ac.RUnlock()
	didCancel := false
	if cdw != nil {
		didCancel = true
		cdw()
	}
	if cc != nil {
		didCancel = true
		cc()
	}
	return didCancel
}

func (ac *client) refresh(ai *manager.AgentPodInfo) {
	var cdw context.CancelFunc
	defer func() {
		if cdw != nil {
			cdw()
		}
	}()

	ac.Lock()
	defer ac.Unlock()

	oldStatus := ac.info.Intercepted
	ac.info = ai
	// Give a previously failed QUIC path a fresh chance on every watch update for this
	// pod; a pod restart in particular gets a new attempt this way.
	ac.quicDead.Store(false)
	if ai.Intercepted == oldStatus {
		if ai.Intercepted && ac.cancelDialWatch == nil {
			clog.Debugf(ac, "Agent %s(%s) is intercepted but has no dial watcher; ensuring connection", ai.PodName, net.IP(ai.PodIp))
			if _, err := ac.ensureConnectLocked(ac); err != nil {
				clog.Errorf(ac, "failed to ensure client watcher for %s(%s): %v", ai.PodName, net.IP(ai.PodIp), err)
			}
		}
		return
	}
	if ai.Intercepted {
		clog.Debugf(ac, "Agent %s(%s) changed to intercepted", ai.PodName, net.IP(ai.PodIp))
		if _, err := ac.ensureConnectLocked(ac); err != nil {
			clog.Errorf(ac, "failed to start client watcher for %s(%s): %v", ai.PodName, net.IP(ai.PodIp), err)
		}
	} else {
		// This agent is no longer intercepting. Stop the dial watcher
		clog.Debugf(ac, "Agent %s(%s) changed to not intercepted", ai.PodName, net.IP(ai.PodIp))
		cdw = ac.cancelDialWatch
	}
}

func (ac *client) startDialWatcherLocked() {
	if ac.cancelDialWatch != nil {
		// Already started
		return
	}
	ctx, cancel := context.WithCancel(ac)
	ac.dialWatchID++
	watchID := ac.dialWatchID
	ac.cancelDialWatch = func() {
		ac.Lock()
		if ac.dialWatchID == watchID {
			ac.cancelDialWatch = nil
		}
		ac.Unlock()
		cancel()
	}

	go ac.runDialWatcher(ctx, watchID)
}

func (ac *client) resetAgentClient() {
	ac.Lock()
	cancelClient := ac.cancelClient
	ac.cancelClient = nil
	ac.cli = nil
	atomic.StoreInt32(&ac.tunnelCount, 0)
	ac.Unlock()
	if cancelClient != nil {
		cancelClient()
	}
}

func (ac *client) runDialWatcher(ctx context.Context, watchID uint64) {
	defer func() {
		ac.Lock()
		if ac.dialWatchID == watchID {
			ac.cancelDialWatch = nil
		}
		ac.Unlock()
	}()

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = dialWatcherReconnectInitial
	bo.MaxInterval = dialWatcherReconnectMax
	bo.MaxElapsedTime = 0
	bo.Reset()
	lastConnected := time.Now()

	for ctx.Err() == nil {
		cli, err := ac.ensureConnect(ctx)
		if err == nil {
			ac.RLock()
			session := ac.session
			ai := ac.info
			ac.RUnlock()

			clog.Debugf(ctx, "watching dials from agent pod %s(%s)", ai.PodName, net.IP(ai.PodIp))
			dialStream, watchErr := cli.WatchDial(ctx, session)
			if watchErr == nil {
				var metrics tunnel.DialMetrics
				if ac.owner != nil {
					metrics = ac.owner.loadDialMetrics()
				}
				if time.Since(lastConnected) > dialWatcherReconnectResetTime {
					bo.Reset()
				}
				lastConnected = time.Now()
				watchErr = tunnel.DialWaitLoop(ctx, tunnel.AgentToClient, tunnel.AgentProvider(cli), dialStream, tunnel.SessionID(session.SessionId), metrics)
			}
			err = watchErr
		}
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			clog.Warnf(ctx, "dial watcher for %s ended; reconnecting: %v", ac, err)
			ac.resetAgentClient()
		} else {
			clog.Warnf(ctx, "dial watcher for %s ended unexpectedly; reconnecting", ac)
		}

		delay := bo.NextBackOff()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type Clients interface {
	GetRandomAgent(context.Context) agent.AgentClient
	GetClient(netip.Addr) tunnel.Provider
	WatchAgentPods(rmc manager.ManagerClient) error

	// ApplyPodsDelta applies one agent-pod delta pushed by the user daemon. When reset
	// is true, all previously applied state is discarded first.
	ApplyPodsDelta(reset bool, upserts map[string]*manager.AgentPodInfo, removals []string) error

	// RunDeltaSink is the relay-mode counterpart of WatchAgentPods: it records the
	// manager client used for lazy QUIC endpoint fetches, then blocks until the
	// session ends, performing the same teardown as WatchAgentPods on exit.
	RunDeltaSink(rmc manager.ManagerClient) error

	WaitForIP(ctx context.Context, timeout time.Duration, namespace string, ip netip.Addr) error
	WaitForWorkload(timeout time.Duration, name string) error
	GetWorkloadClient(workload string) (ag tunnel.Provider)
	SetProxyVia(workload string)

	// WorkloadForIP returns the name and namespace of the workload whose traffic-agent
	// runs in the pod with the given IP, as reported by the most recent agent-pod
	// snapshot.
	WorkloadForIP(ip netip.Addr) (workload, namespace string, ok bool)

	// SetDialMetrics installs a DialMetrics implementation that receives a
	// callback for every dial request the dial watchers accept. Passing nil
	// disables the callback. Safe to call at any time.
	SetDialMetrics(m tunnel.DialMetrics)

	// SetChangeListener installs a callback that is invoked whenever an agent
	// pod is added to or removed from the watched set. Passing nil disables the
	// callback. Safe to call at any time.
	SetChangeListener(func())

	// SetPreferredQuicAddr installs the callback used to resolve the forwarder
	// address for agent QUIC connections, in place of the QUIC tunnel endpoint
	// descriptor's own host/port. See the method doc on *clients for the intended
	// use (sharing the manager-bound QUIC tunnel's winning candidate). Passing nil
	// reverts to the descriptor's own host/port. Safe to call at any time.
	SetPreferredQuicAddr(func() string)

	// ResetQuicEndpoint clears the cached QUIC tunnel endpoint descriptor and every
	// agent's quicDead latch, so the next agent dial fetches a fresh descriptor and
	// is willing to try QUIC again rather than going straight to the port-forward
	// fallback.
	ResetQuicEndpoint()

	// Transports returns the current transport ("quic" or "grpc") for every agent pod
	// that has ever completed a connection attempt this session. An agent that hasn't
	// been dialed yet is omitted; the status surface renders this list only when
	// non-empty.
	Transports() []AgentTransport
}

// AgentTransport reports which transport currently carries the live connection to one
// connected agent pod, for the status surface.
type AgentTransport struct {
	Workload  string
	Pod       string
	Transport string // "quic" or "grpc"
}

type ipWaitKey struct {
	namespace string
	ip        netip.Addr
}

type clients struct {
	*k8s.Cluster
	session      *manager.SessionInfo
	clients      *xsync.Map[string, *client]
	ipWaiters    *xsync.Map[ipWaitKey, chan struct{}]
	wlWaiters    *xsync.Map[string, chan struct{}]
	proxyVias    *xsync.Map[string, struct{}]
	namespacesMu sync.RWMutex
	namespaces   map[string]struct{}
	disabled     atomic.Bool

	// snapshot is the set of agent pods most recently reported by the watch, keyed by
	// "<podName>.<namespace>". Unlike the live clients map, it is not mutated by failed dial
	// attempts, so WaitForIP can consult it to tell whether an agent still exists and should be
	// retried. Guarded by snapshotMu.
	snapshotMu sync.RWMutex
	snapshot   map[string]*manager.AgentPodInfo

	// deltaMu guards deltaSnapshot, the snapshot map maintained by ApplyPodsDelta.
	// Separate from snapshotMu (and from the self-watch mode's local snapMap in
	// WatchAgentPods) because relay pushes can race with the user daemon's relay
	// stream being re-established.
	deltaMu       sync.Mutex
	deltaSnapshot map[string]*manager.AgentPodInfo

	// dialMetrics, when non-nil, receives a callback for every dial
	// request accepted (or rejected) by a started dial watcher. Guarded
	// by dialMetricsMu so SetDialMetrics can race safely with watcher
	// creation.
	dialMetricsMu sync.RWMutex
	dialMetrics   tunnel.DialMetrics

	// changeListener, when non-nil, is invoked whenever an agent pod is added
	// to or removed from the watched set. Guarded by changeListenerMu.
	changeListenerMu sync.RWMutex
	changeListener   func()

	// mcMu guards mc, the manager client used to lazily fetch the QUIC tunnel endpoint
	// descriptor for agent connections. Set once WatchAgentPods starts.
	mcMu sync.RWMutex
	mc   manager.ManagerClient

	// quicEP lazily fetches and caches the QUIC tunnel endpoint descriptor, at most once
	// per connector session, the first time an agent that advertises a quic_sni is dialed.
	quicEP quicEndpointCache

	// preferredAddr, when set, returns the forwarder address the manager-bound QUIC
	// tunnel already found reachable this session (see SetPreferredQuicAddr); a nil
	// func or an empty return means none is known yet, and quicEndpointFor falls back
	// to the descriptor's own host/port.
	preferredAddrMu sync.RWMutex
	preferredAddr   func() string

	// tokenProvider returns the session token attached to every agent gRPC call as
	// per-RPC credentials (see tokenCredentials); nil, or a provider returning "",
	// means no token is attached, exactly as before this credential existed.
	tokenProvider func(ctx context.Context) string
}

// NewClients returns the Clients implementation that manages direct gRPC connections to
// this session's traffic-agents. tokenProvider is attached to every agent connection as
// its per-RPC session token (see tokenCredentials); pass nil where no session credential
// is available (e.g. tests).
func NewClients(
	cl *k8s.Cluster, session *manager.SessionInfo, namespaces []string, tokenProvider func(ctx context.Context) string,
) Clients {
	if len(namespaces) == 0 {
		namespaces = []string{cl.Namespace}
	}
	cs := &clients{
		Cluster:       cl,
		session:       session,
		clients:       xsync.NewMap[string, *client](),
		ipWaiters:     xsync.NewMap[ipWaitKey, chan struct{}](),
		wlWaiters:     xsync.NewMap[string, chan struct{}](),
		proxyVias:     xsync.NewMap[string, struct{}](),
		tokenProvider: tokenProvider,
	}
	// One TLS session cache for every agent QUIC dial this connector session ever makes:
	// tls.ClientSessionCache is keyed by ServerName, so a single LRU instance shared
	// across every agent's quic_sni is enough for each to resume independently. It must
	// not outlive this *clients (a fresh connector session presents a different
	// session-scoped client certificate, and a ticket resumed under the old one would
	// simply be rejected by the manager's per-stream session check).
	cs.quicEP.cache = tls.NewLRUClientSessionCache(16)
	cs.setNamespaces(namespaces)
	return cs
}

func (s *clients) setNamespaces(namespaces []string) {
	nsSet := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if namespace != "" {
			nsSet[namespace] = struct{}{}
		}
	}
	s.namespacesMu.Lock()
	s.namespaces = nsSet
	s.namespacesMu.Unlock()
}

func (s *clients) namespaceList() []string {
	s.namespacesMu.RLock()
	namespaces := make([]string, 0, len(s.namespaces))
	for namespace := range s.namespaces {
		namespaces = append(namespaces, namespace)
	}
	s.namespacesMu.RUnlock()
	return namespaces
}

func (s *clients) watchesNamespace(namespace string) bool {
	if namespace == "" {
		namespace = s.Namespace
	}
	s.namespacesMu.RLock()
	_, ok := s.namespaces[namespace]
	s.namespacesMu.RUnlock()
	return ok
}

// GetClient returns tunnel.Provider that opens a tunnel to a known traffic-agent.
// The traffic-agent is chosen using the following rules in the order mentioned:
//
//  1. agent has a pod_ip that matches the given ip
//  2. agent is currently intercepted by this client
//  3. any agent
//
// The function returns nil when there are no agents in the connected namespace.
func (s *clients) GetClient(ip netip.Addr) (pvd tunnel.Provider) {
	if s.disabled.Load() {
		return nil
	}
	var primary, secondary, ternary tunnel.Provider
	s.clients.Range(func(_ string, c *client) bool {
		podIP, ok := netip.AddrFromSlice(c.info.PodIp)
		switch {
		case ok && ip == podIP:
			primary = c
		case c.intercepted():
			secondary = c
		default:
			ternary = c
		}
		return primary == nil
	})
	switch {
	case primary != nil:
		pvd = primary
	case secondary != nil:
		pvd = secondary
	default:
		pvd = ternary
	}
	return pvd
}

// GetRandomAgent returns an active agent.AgentClient and ensures that it is kept alive
// for at least 5 seconds.
//
// Node-agent sessions are never returned. A node-agent's pod runs in the
// traffic-manager's namespace rather than the workload's, so its resolv.conf
// search path qualifies bare (single-label) names against the wrong
// namespace and DNS lookups delegated to it would incorrectly fail. When no
// eligible (non-node-agent) client remains, this function returns nil and
// the caller falls back to querying the traffic-manager directly, which
// qualifies single-label names against the client's connected namespace
// itself.
//
// The function returns nil when there are no active agents.
func (s *clients) GetRandomAgent(ctx context.Context) (aa agent.AgentClient) {
	var connected, waiting, other *client
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.info.NodeAgent {
			return true
		}
		if ac.connected() {
			connected = ac
			return false
		}
		if s.isProxyVIA(ac.info) || s.hasWaiterFor(ac.info) {
			waiting = ac
		} else {
			other = ac
		}
		return true
	})

	var err error
	switch {
	case connected != nil:
		connected.Lock()
		connected.lastActive = time.Now().UnixNano()
		aa = connected.cli
		connected.Unlock()
	case waiting != nil:
		aa, err = waiting.ensureConnect(ctx)
	case other != nil:
		aa, err = other.ensureConnect(ctx)
	}
	if err != nil {
		clog.Warn(s, err)
	}
	return aa
}

// GetWorkloadClient returns tunnel.Provider that opens a tunnel to a traffic-agent that
// belongs to a pod created for the given workload.
//
// Proxy-via routing remains scoped to the connected namespace. The function returns nil
// when there are no agents for the given workload in that namespace.
func (s *clients) GetWorkloadClient(workload string) (pvd tunnel.Provider) {
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.info.WorkloadName == workload && ac.info.Namespace == s.Namespace {
			pvd = ac
			return false
		}
		return true
	})
	return pvd
}

func (s *clients) SetProxyVia(workload string) {
	s.proxyVias.Store(workload, struct{}{})
}

func (s *clients) SetDialMetrics(m tunnel.DialMetrics) {
	s.dialMetricsMu.Lock()
	s.dialMetrics = m
	s.dialMetricsMu.Unlock()
}

func (s *clients) loadDialMetrics() tunnel.DialMetrics {
	s.dialMetricsMu.RLock()
	m := s.dialMetrics
	s.dialMetricsMu.RUnlock()
	return m
}

func (s *clients) SetChangeListener(f func()) {
	s.changeListenerMu.Lock()
	s.changeListener = f
	s.changeListenerMu.Unlock()
}

// notifyChanged invokes the change listener, if one is installed. It is called when an agent pod is
// added to or removed from the watched set.
func (s *clients) notifyChanged() {
	s.changeListenerMu.RLock()
	f := s.changeListener
	s.changeListenerMu.RUnlock()
	if f != nil {
		f()
	}
}

func (s *clients) isProxyVIA(info *manager.AgentPodInfo) bool {
	_, isPV := s.proxyVias.Load(info.WorkloadName)
	return isPV
}

func (s *clients) hasWaiterFor(info *manager.AgentPodInfo) bool {
	if podIP, ok := netip.AddrFromSlice(info.PodIp); ok {
		if _, isW := s.ipWaiters.Load(ipWaitKey{namespace: info.Namespace, ip: podIP}); isW {
			return true
		}
	}
	if _, isW := s.wlWaiters.Load(info.WorkloadName); isW {
		return true
	}
	return false
}

// setManagerClient records the manager client used to lazily fetch the QUIC tunnel
// endpoint descriptor for agent connections.
func (s *clients) setManagerClient(mc manager.ManagerClient) {
	s.mcMu.Lock()
	s.mc = mc
	s.mcMu.Unlock()
}

func (s *clients) managerClient() manager.ManagerClient {
	s.mcMu.RLock()
	defer s.mcMu.RUnlock()
	return s.mc
}

// SetPreferredQuicAddr installs the callback quicEndpointFor consults for the forwarder
// address to use, in place of the QUIC tunnel endpoint descriptor's own host/port. f is
// typically a closure over the rootd session's manager-bound QUIC connection, returning
// "" until that connection's own candidate probe has picked a winner. Passing nil (the
// default) makes agent connections use the descriptor's host/port directly.
func (s *clients) SetPreferredQuicAddr(f func() string) {
	s.preferredAddrMu.Lock()
	s.preferredAddr = f
	s.preferredAddrMu.Unlock()
}

func (s *clients) preferredQuicAddr() string {
	s.preferredAddrMu.RLock()
	f := s.preferredAddr
	s.preferredAddrMu.RUnlock()
	if f == nil {
		return ""
	}
	return f()
}

// quicEndpointFor returns the traffic-manager's QUIC tunnel endpoint descriptor for agent
// connections, fetching and caching it (including negative results) on first use. Returns
// nil when QUIC isn't usable this session, or when no manager client is available yet (in
// which case the fetch is left unattempted rather than cached as a false negative).
func (s *clients) quicEndpointFor(ctx context.Context) *quicEndpoint {
	return s.quicEP.get(ctx, s.managerClient(), s.session, s.preferredQuicAddr())
}

// ResetQuicEndpoint implements Clients.
func (s *clients) ResetQuicEndpoint() {
	s.quicEP.reset()
	s.clients.Range(func(_ string, ac *client) bool {
		ac.quicDead.Store(false)
		return true
	})
}

// Transports implements Clients.
func (s *clients) Transports() []AgentTransport {
	var ts []AgentTransport
	s.clients.Range(func(_ string, ac *client) bool {
		if tr, _ := ac.transport.Load().(string); tr != "" {
			ac.RLock()
			ai := ac.info
			ac.RUnlock()
			ts = append(ts, AgentTransport{Workload: ai.WorkloadName, Pod: ai.PodName, Transport: tr})
		}
		return true
	})
	return ts
}

func (s *clients) WatchAgentPods(rmc manager.ManagerClient) error {
	s.setManagerClient(rmc)
	defer s.teardown()

	snapMap := make(map[string]*manager.AgentPodInfo)
	return WatchPods(s, rmc, s.session, s.namespaceList(), s.Namespace,
		func(upserts map[string]*manager.AgentPodInfo, removals []string) error {
			maps.DeltaUpdate(snapMap, upserts, removals)
			return s.updateClients(maps.Values(snapMap))
		},
		func() error {
			clear(snapMap)
			return nil
		},
		s.setNamespaces)
}

// ApplyPodsDelta implements Clients. It applies one agent-pod delta pushed by the user
// daemon through the relay. Guarded by deltaMu because relay pushes can race with the
// user daemon's relay stream being re-established.
func (s *clients) ApplyPodsDelta(reset bool, upserts map[string]*manager.AgentPodInfo, removals []string) error {
	s.deltaMu.Lock()
	defer s.deltaMu.Unlock()
	if s.deltaSnapshot == nil {
		s.deltaSnapshot = make(map[string]*manager.AgentPodInfo)
	}
	if reset {
		clear(s.deltaSnapshot)
	}
	maps.DeltaUpdate(s.deltaSnapshot, upserts, removals)
	return s.updateClients(maps.Values(s.deltaSnapshot))
}

// RunDeltaSink implements Clients. It is the relay-mode counterpart of WatchAgentPods:
// it records the manager client used for lazy QUIC endpoint fetches, then blocks until
// the session ends, performing the same teardown as WatchAgentPods on exit.
func (s *clients) RunDeltaSink(rmc manager.ManagerClient) error {
	s.setManagerClient(rmc)
	defer s.teardown()
	<-s.Done()
	return nil
}

// teardown cancels every live agent client and disables further use of this Clients
// instance. It runs when the watch (self-watching or relay) that feeds this instance
// ends, whichever mode that is.
func (s *clients) teardown() {
	activeCount := 0
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.cancel() {
			activeCount++
		}
		return true
	})
	clog.Debugf(s, "WatchAgentPods ending with %d clients still active", activeCount)
	s.disabled.Store(true)
}

func (ac *client) notify(waiter chan struct{}) {
	// a client must be connected to be able to notify
	if _, err := ac.ensureConnect(ac); err != nil {
		clog.Errorf(ac, "notifyWaiters %s (%s), ensureConnect failed: %v", ac.info.WorkloadName, net.IP(ac.info.PodIp), err)
	}
	close(waiter)
}

func (s *clients) notifyWaiters() {
	s.clients.Range(func(name string, ac *client) bool {
		if podIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok {
			if waiter, ok := s.ipWaiters.LoadAndDelete(ipWaitKey{namespace: ac.info.Namespace, ip: podIP}); ok {
				ac.notify(waiter)
			}
		}
		if waiter, ok := s.wlWaiters.LoadAndDelete(ac.info.WorkloadName); ok {
			ac.notify(waiter)
		}
		return true
	})
}

func (s *clients) waitWithTimeout(timeout time.Duration, waitOn <-chan struct{}) error {
	s.notifyWaiters()
	ctx, cancel := context.WithTimeout(s, timeout)
	defer cancel()
	select {
	case <-waitOn:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// snapshotInfoForIP returns the AgentPodInfo for the given namespace and pod IP from the latest
// watch snapshot, or nil if the watch doesn't (currently) know of such an agent.
func (s *clients) snapshotInfoForIP(namespace string, ip netip.Addr) *manager.AgentPodInfo {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	for _, ai := range s.snapshot {
		if podIP, ok := netip.AddrFromSlice(ai.PodIp); ok && ai.Namespace == namespace && ip == podIP {
			return ai
		}
	}
	return nil
}

// WorkloadForIP returns the name and namespace of the workload whose traffic-agent
// runs in the pod with the given IP, as reported by the most recent agent-pod snapshot.
func (s *clients) WorkloadForIP(ip netip.Addr) (workload, namespace string, ok bool) {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	for _, ai := range s.snapshot {
		if podIP, aok := netip.AddrFromSlice(ai.PodIp); aok && podIP == ip {
			return ai.WorkloadName, ai.Namespace, true
		}
	}
	return "", "", false
}

// loadOrAddClient returns the live client for the given agent pod, adding it if the (delta-based)
// agent watch has not (re)added it. A failed dial removes a client from the live set, and because
// the watch only emits on change it may not re-add it; this lets WaitForIP retry the dial for an
// agent that still exists in the watch snapshot.
func (s *clients) loadOrAddClient(ai *manager.AgentPodInfo) *client {
	k := ai.PodName + "." + ai.Namespace
	ac, _ := s.clients.LoadOrCompute(k, func() (*client, bool) {
		return &client{
			Cluster: s.Cluster,
			session: s.session,
			remove:  func() { s.clients.Delete(k) },
			owner:   s,
			info:    ai,
		}, false
	})
	return ac
}

func (s *clients) WaitForIP(ctx context.Context, timeout time.Duration, namespace string, ip netip.Addr) error {
	if s.disabled.Load() {
		return status.Error(codes.Unavailable, "")
	}
	if namespace == "" {
		namespace = s.Namespace
	}
	if !s.watchesNamespace(namespace) {
		return status.Error(codes.Unavailable, "")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Register an IP waiter so the agent watch wakes us promptly when the pod is reported, and so a
	// dormant client for this IP isn't reaped while we wait (see hasWaiterFor).
	key := ipWaitKey{namespace: namespace, ip: ip}
	waitOn, _ := s.ipWaiters.LoadOrCompute(key, func() (chan struct{}, bool) {
		return make(chan struct{}), false
	})
	defer s.ipWaiters.Delete(key)

	// Retry until the agent is actually reachable or the deadline expires. The first dial to a
	// just-restarted pod can lose a race (e.g. the route to its new IP isn't ready yet); on failure
	// ensureConnect removes the client, and because the watch is delta-based it won't necessarily
	// re-add it. We therefore drive retries off the watch snapshot - which a failed dial does not
	// mutate - re-adding the live client ourselves as needed.
	for {
		if ai := s.snapshotInfoForIP(namespace, ip); ai != nil {
			if _, err := s.loadOrAddClient(ai).ensureConnect(ctx); err == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(connectRetryInterval):
			}
			continue
		}
		// The agent isn't in the snapshot yet. Wait to be woken by the watch; the timer is a
		// fallback in case the wakeup is missed.
		select {
		case <-waitOn:
			// notifyWaiters closes and deletes the waiter, so re-arm it for any subsequent round.
			waitOn, _ = s.ipWaiters.LoadOrCompute(key, func() (chan struct{}, bool) {
				return make(chan struct{}), false
			})
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(connectRetryInterval):
		}
	}
}

func (s *clients) WaitForWorkload(timeout time.Duration, name string) error {
	if s.disabled.Load() {
		return nil
	}

	// Create a channel to subscribe to, but only if the agent doesn't already exist.
	waitOn, ok := s.wlWaiters.LoadOrCompute(name, func() (chan struct{}, bool) {
		found := false
		s.clients.Range(func(k string, ac *client) bool {
			if ac.info.WorkloadName == name {
				found = true
				return false
			}
			return true
		})
		if found {
			return nil, true
		}
		return make(chan struct{}), false
	})
	if ok {
		return s.waitWithTimeout(timeout, waitOn)
	}
	// No chan created because the agent already exists
	return nil
}

func (s *clients) updateClients(ais []*manager.AgentPodInfo) error {
	defer s.notifyWaiters()

	var aim map[string]*manager.AgentPodInfo
	if len(ais) > 0 {
		aim = make(map[string]*manager.AgentPodInfo, len(ais))
		for _, ai := range ais {
			if ai.PodName != "" {
				aim[ai.PodName+"."+ai.Namespace] = ai
			}
		}
		if len(aim) == 0 {
			// The current traffic-manager injects old style clients that doesn't report a pod name.
			clog.Debugf(s, "disabling, because traffic-agent doesn't report pod name")
			s.disabled.Store(true)
			return nil
		}
	}

	// Record the watch's view of existing agents. WaitForIP consults this rather than the live
	// clients map, because a failed dial removes a client from the live map but the agent still
	// exists here until the watch reports it gone.
	s.snapshotMu.Lock()
	s.snapshot = aim
	s.snapshotMu.Unlock()

	// changed is set when the membership of the watched agent set changes, so the DNS cache can be
	// flushed - a recreated workload may have a recreated Service with a new ClusterIP.
	changed := false
	deleteClient := func(k string) {
		s.clients.Compute(k, func(oldValue *client, loaded bool) (*client, xsync.ComputeOp) {
			if loaded {
				clog.Debugf(s, "Deleting agent %s", k)
				oldValue.cancel()
				changed = true
				return nil, xsync.DeleteOp
			}
			return nil, xsync.CancelOp
		})
	}

	// Cancel clients that no longer exist.
	s.clients.Range(func(k string, _ *client) bool {
		if _, ok := aim[k]; !ok {
			deleteClient(k)
		}
		return true
	})

	// Refresh current clients
	for k, ai := range aim {
		if ac, ok := s.clients.Load(k); ok {
			ac.refresh(ai)
		}
	}

	addClient := func(k string, ai *manager.AgentPodInfo) {
		ac, loaded := s.clients.LoadOrCompute(k, func() (*client, bool) {
			ac := &client{
				Cluster: s.Cluster,
				session: s.session,
				remove: func() {
					s.clients.Delete(k)
				},
				owner: s,
				info:  ai,
			}
			clog.Debugf(s, "Adding agent pod %s (%s)", k, net.IP(ai.PodIp))
			return ac, false
		})
		if !loaded {
			changed = true
		}
		if !loaded && ai.Intercepted {
			clog.Debugf(s, "Newly discovered intercepted agent pod %s (%s); eagerly starting dial watcher", k, net.IP(ai.PodIp))
			if _, err := ac.ensureConnect(s); err != nil {
				clog.Errorf(s, "failed to eagerly start client watcher for %s (%s): %v", k, net.IP(ai.PodIp), err)
			}
		}
	}

	// Add clients for newly arrived agents.
	for k, ai := range aim {
		addClient(k, ai)
	}

	// Terminate all dormant agents except the last one.
	dormantCount := 0
	s.clients.Range(func(k string, ac *client) bool {
		if ac.dormant() && !s.isProxyVIA(ac.info) && !s.hasWaiterFor(ac.info) {
			dormantCount++
			if dormantCount > 1 {
				clog.Debugf(s, "Deleting dormant agent %s", k)
				ac.cancel()
			}
		}
		return true
	})
	if dormantCount > 1 {
		clog.Debugf(s, "Cancelled %d dormant clients", dormantCount-1)
	}
	if changed {
		s.notifyChanged()
	}
	return nil
}
