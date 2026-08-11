package rootd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver/v4"
	dns2 "github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/agentpf"
	"github.com/telepresenceio/telepresence/v2/pkg/client/bwcompat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/teleroute"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/vip"
	"github.com/telepresenceio/telepresence/v2/pkg/client/sessioncred"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type agentSubnet struct {
	netip.Prefix
	workload string
}

type agentVIP struct {
	workload      string
	destinationIP netip.Addr
}

// session resolves DNS names and routes outbound traffic that is centered around a TUN device. The router is
// similar to a TUN-to-SOCKS5 but uses a bidirectional gRPC muxTunnel instead of SOCKS when communicating with the
// traffic-manager. The addresses of the device are derived from IP addresses sent to it from the user
// daemon (which in turn receives them from the cluster).
//
// Data sent to the device is received as L3 IP-packets and parsed into L4 UDP and TCP before they
// are dispatched over the muxTunnel. Returned payloads are wrapped as IP-packets before written
// back to the device. This L3 <=> L4 conversation is made using gvisor.dev/gvisor/pkg/tcpip.
//
// Connection pooling:
//
// For UDP and TCP packets, a ConnID is created which uniquely identifies a combination of protocol,
// source IP, source port, destination IP, and destination port. A handler is then obtained that matches
// that ID (active handlers are cached in a tunnel.Pool) and the packet is then sent to that handler.
// The handler typically sends the ConnID and the payload of the packet over to the traffic-manager
// using the gRPC ClientTunnel. At the receiving en din the traffic-manager, a similar tunnel.Pool obtains
// a corresponding handler which manages a net.Conn matching the ConnID in the cluster.
//
// A zero session is invalid; you must use the createSession or the newSession function.
type session struct {
	*k8s.Cluster

	tunVif *vif.TunnelingDevice

	teleroute teleroute.Server

	// managerConnMu guards managerConn, ownsManagerConn, and managerVersion.
	// The connection is pinned to one manager pod; when that pod goes away,
	// reconnectManager replaces it with a connection to a freshly resolved pod.
	managerConnMu sync.Mutex

	// managerConn is the connection to the traffic-manager.
	managerConn *grpc.ClientConn

	// ownsManagerConn is false when managerConn was borrowed from the user
	// daemon (the in-process root session); a borrowed connection is never
	// closed by reconnectManager. Connections created by reconnectManager are
	// always owned.
	ownsManagerConn bool

	// managerNamespace is the namespace the traffic-manager is installed in,
	// needed to resolve a new manager pod on reconnect.
	managerNamespace string

	// agentClients provides direct gRPC tunnels to traffic-agents in namespaces where the client can port-forward.
	agentClients agentpf.Clients

	// sessionCredential caches the session-scoped credential fetched from the manager;
	// see sessionCredentialToken, which agentClients uses as its per-RPC token provider.
	sessionCredential sessioncred.Cache

	// managerVersion is the version of the connected traffic-manager
	managerVersion semver.Version

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *tunnel.Pool

	// The local dns server
	dnsServer *dns.Server

	// vifDNS is the address and port of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd-resolved. The current macOS and the overriding solution
	// will dispatch directly to the local DNS Service without going through the TUN device, but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	vifDNS netip.AddrPort

	// localDNS is the address and port of the local DNS Service.
	localDNS netip.AddrPort

	// serviceSubnets reported by the traffic-manager
	serviceSubnets []netip.Prefix

	// podSubnets reported by the traffic-manager
	podSubnets []netip.Prefix

	// Subnets configured by the user
	alsoProxySubnets []netip.Prefix

	// Subnets configured by the user to never be proxied
	neverProxySubnets []netip.Prefix

	// Like neverProxySubnets but stripped from the ones that aren't proxied anyway
	effectiveNeverProxy []netip.Prefix

	// Subnets that will be mapped even if they conflict with local routes
	allowConflictingSubnets []netip.Prefix

	// localTranslationTable maps an IP returned by the cluster's DNS to a virtual IP created by this server.
	localTranslationTable *xsync.Map[netip.Addr, netip.Addr]

	// IP addresses that the cluster's DNS resolves that are contained in one of the subnets in this
	// slice are translated to a virtual IP (cached in the localTranslationTable)
	localTranslationSubnets []agentSubnet

	// virtualIPs maps a virtual IP to an agent tunnel.
	virtualIPs *xsync.Map[netip.Addr, agentVIP]

	// vipGenerator allocates virtual IPs per address family for proxy-via and
	// conflict resolution.
	vipGenerator *vip.Generators

	// closing is set during shutdown and can have the values:
	//   0 = running
	//   1 = closing
	//   2 = closed
	closing int32

	// session contains the manager session
	session *manager.SessionInfo

	// rndSource is the source for the random number generator in the TCP handlers
	rndSource rand.Source

	// Telemetry counters for DNS lookups
	dnsLookups  int
	dnsFailures int

	// Whether pods should be proxied by the TUN-device
	proxyClusterPods bool

	// Whether services should be proxied by the TUN-device
	proxyClusterSvcs bool

	// dnsServerSubnet is normally never set. It is only used when neither proxyClusterPods nor the
	// proxyClusterSvcs are set. In this situation, the VIF would be left without a primary subnet, so
	// it will instead route very small subnet with 30 bit mask, large enough to hold:
	//
	//   n.n.n.0 The IP identifying the subnet
	//   n.n.n.1 The IP of the (non existent) gateway
	//   n.n.n.2 The IP of the DNS server
	//   n.n.n.3 Unused
	//
	// The subnet is guaranteed to be free from all other routed subnets.
	//
	// NOTE: On macOS, where DNS is controlled by adding entries in /etc/resolver that points directly
	// to a port on localhost, there's no need for this subnet.
	dnsServerSubnet netip.Prefix

	// vifReady is closed when the virtual network interface has been configured.
	vifReady chan error

	subnetViaWorkloads []*rpc.SubnetViaWorkload

	// agentPodNamespaces are the namespaces in which the user daemon relays agent-pod
	// events using the daemon.Daemon WatchAgentPods RPC. When non-empty, this daemon
	// does not watch agent pods itself; see session.Start.
	agentPodNamespaces []string

	// daemon runs as part of a pod-daemon setup.
	podDaemon bool
	routesCh  chan []netip.Prefix

	// Timestamps sent on this channel are propagated to the user daemon.
	activity chan<- time.Time

	// sessionStart is when this session was constructed. Used to compute
	// the duration reported in the session-end Activity message.
	sessionStart time.Time

	// Telemetry counters reported to the user daemon at session end via the
	// final daemon.Activity message. Touched from multiple goroutines.
	outboundTunnels      atomic.Int64
	outboundTunnelErrors atomic.Int64
	incomingDials        atomic.Int64
	incomingDialErrors   atomic.Int64

	// Maps one UDP or TCP AddrPort to another
	l4PortMap *xsync.Map[types.AddrPortProto, uint16]

	// Cluster-side destinations that are connected directly to local intercept
	// handlers instead of being tunneled to the cluster. Nil when there are none.
	interceptShortcuts atomic.Pointer[shortcutTable]

	// Redirects a remote UDP or TCP AddrPort to a local host port.
	localClientRedirects *xsync.Map[types.AddrPortProto, netip.AddrPort]

	lookupSequencer *xsync.Map[string, clusterLookupResult]

	// quicConn is the client's current QUIC connection to the traffic-manager, set
	// whenever a dial (initial or re-probe) succeeds. Nil for the lifetime of the
	// session when QUIC was never dialed (older manager, endpoint disabled, or the dial
	// failed); the tunnel then stays on the port-forwarded gRPC path. Written from the
	// "quic" goroutine started in Start, and from quicReprobeLoop after a later
	// recovery; read from streamCreator and stop, hence atomic.
	quicConn atomic.Pointer[quic.Conn]

	// quicTunnelProvider serves manager-bound tunnel streams over QUIC while healthy,
	// falling back to the port-forwarded gRPC connection once the installed
	// quicFallbackProvider trips. Nil when quicConn is nil. quicReprobeLoop retries the
	// dial after a trip and, on success, replaces this pointer with a fresh provider
	// (see quicFallbackProvider's doc). See quicConn for the concurrency note.
	quicTunnelProvider atomic.Pointer[quicFallbackProvider]

	// quicReprobeTrigger receives a value each time quicTunnelProvider trips to
	// fallback (sent by onQuicFallback), waking quicReprobeLoop. Buffered 1 with
	// non-blocking sends: a trip that happens while a probe is already in flight is
	// coalesced into the retry already running, never lost and never blocking the
	// tripping goroutine. Initialized in newSession; nil only in tests that construct a
	// bare session{} and never trip a provider.
	quicReprobeTrigger chan struct{}

	// quicSessionCache is the TLS session cache attached to every QUIC dial this session
	// makes to the traffic-manager (initial dial and every reprobe retry alike), so a
	// reconnect within the session's lifetime can resume instead of paying a full TLS 1.3
	// handshake. One instance per session, never global: a resumed ticket carries this
	// session's client identity (cert CN = session ID), so it must not survive into a new
	// telepresence session. Initialized in newSession; nil only in tests that construct a
	// bare session{}, in which case quicTLSConfig simply dials without resumption.
	quicSessionCache tls.ClientSessionCache

	// datagramCounters accumulates RFC 9221 datagram sent/received/fallback/unknown-conn
	// totals across every QUIC connection this session ever activates (initial dial and
	// every reprobe recovery share this one instance), so the summary logged at session
	// end in stop() covers the whole session rather than just its last connection.
	datagramCounters *tunnel.DatagramCounters

	// transportStatus is the observable tunnel transport for manager-bound streams. A
	// nil pointer means the default steady state: gRPC, because QUIC was never dialed
	// (older manager, endpoint disabled, unimplemented, or dial failed). Replaced, never
	// mutated, by the "quic" goroutine on a successful dial and by onQuicFallback /
	// quicReprobeLoop on every later transition; see TransportStatus and
	// setTransportStatus.
	transportStatus atomic.Pointer[transportStatus]
}

// Observable values of transportStatus.transport, as reported by TransportStatus and
// surfaced in the daemon Status RPC and usage reports.
const (
	TransportGRPC         = "grpc"
	TransportQUIC         = "quic"
	TransportGRPCFallback = "grpc (fallback)"
)

// transportStatus is the value stored in session.transportStatus. Immutable once
// stored, so concurrent readers of the atomic.Pointer never observe a half-written
// value.
type transportStatus struct {
	transport string
	endpoint  string // remote QUIC endpoint address; only set when transport is TransportQUIC
}

// TransportStatus returns the tunnel transport currently serving manager-bound tunnel
// streams ("grpc" by default) and, when it is "quic", the remote endpoint address.
func (s *session) TransportStatus() (transport, endpoint string) {
	if ts := s.transportStatus.Load(); ts != nil {
		return ts.transport, ts.endpoint
	}
	return TransportGRPC, ""
}

// setTransportStatus replaces the observable transport state.
func (s *session) setTransportStatus(transport, endpoint string) {
	s.transportStatus.Store(&transportStatus{transport: transport, endpoint: endpoint})
}

// tunnelTransportRPC returns the current transport status in the shape the daemon
// Status and Connect RPCs report it in.
func (s *session) tunnelTransportRPC() *rpc.TunnelTransport {
	transport, endpoint := s.TransportStatus()
	return &rpc.TunnelTransport{Transport: transport, Endpoint: endpoint}
}

// agentTransportsRPC returns, in the shape the daemon Status and Connect RPCs report it in,
// which transport ("quic" or "grpc") currently carries the live connection to each
// traffic-agent pod that has completed a connection attempt this session. Returns nil
// (omitted on the wire) when agent port-forwards are disabled or no agent has connected
// yet, so an older CLI simply sees nothing to render.
func (s *session) agentTransportsRPC() []*rpc.AgentTransport {
	if s.agentClients == nil {
		return nil
	}
	ts := s.agentClients.Transports()
	if len(ts) == 0 {
		return nil
	}
	out := make([]*rpc.AgentTransport, len(ts))
	for i, t := range ts {
		out[i] = &rpc.AgentTransport{Workload: t.Workload, Pod: t.Pod, Transport: t.Transport}
	}
	return out
}

// createSession will establish a connection to the traffic-manager and return a new properly initialized session object.
func createSession(
	sessionCtx, dialCtx context.Context,
	mi *rpc.NetworkConfig,
	activity chan<- time.Time,
	cleanupDNSRouting func(context.Context),
) (s *session, err error) {
	clog.Info(sessionCtx, "-- Starting new session")
	cleanupDNSRouting(sessionCtx)
	kc, err := k8s.NewKubeconfig(sessionCtx, true, mi.KubeFlags, mi.ManagerNamespace, mi.KubeconfigData)
	if err != nil {
		return nil, err
	}
	cl, err := k8s.NewCluster(kc, mi.MappedNamespaces)
	if err != nil {
		return nil, err
	}
	conn, _, ver, err := cl.ConnectToManager(dialCtx, mi.ManagerNamespace)
	if err != nil {
		return nil, err
	}
	return newSession(cl, mi, conn, true, ver, activity, false)
}

func nope() bool { return false }

func newSession(
	cluster *k8s.Cluster,
	mi *rpc.NetworkConfig,
	managerConn *grpc.ClientConn,
	ownsManagerConn bool,
	ver semver.Version,
	activity chan<- time.Time,
	isPodDaemon bool,
) (*session, error) {
	clog.Debugf(cluster, "Creating session with id %v", mi.Session)

	s := &session{
		Cluster:               cluster,
		handlers:              tunnel.NewPool(),
		datagramCounters:      &tunnel.DatagramCounters{},
		rndSource:             rand.NewSource(time.Now().UnixNano()),
		session:               mi.Session,
		managerConn:           managerConn,
		ownsManagerConn:       ownsManagerConn,
		managerNamespace:      mi.ManagerNamespace,
		managerVersion:        ver,
		subnetViaWorkloads:    mi.SubnetViaWorkloads,
		agentPodNamespaces:    mi.AgentPodNamespaces,
		proxyClusterPods:      true,
		proxyClusterSvcs:      true,
		vifReady:              make(chan error, 2),
		routesCh:              make(chan []netip.Prefix, 2),
		activity:              activity,
		podDaemon:             isPodDaemon,
		localTranslationTable: xsync.NewMap[netip.Addr, netip.Addr](),
		virtualIPs:            xsync.NewMap[netip.Addr, agentVIP](),
		l4PortMap:             xsync.NewMap[types.AddrPortProto, uint16](),
		sessionStart:          time.Now(),
		quicReprobeTrigger:    make(chan struct{}, 1),
		quicSessionCache:      tls.NewLRUClientSessionCache(16),
		localClientRedirects:  xsync.NewMap[types.AddrPortProto, netip.AddrPort](),
	}
	cfg := client.GetConfig(s)

	// Use simple lookups unless the traffic-manager version is less than 2.25.0 (not supported), or if the user has explicitly
	// requested complex lookups. The presence of the s.lookupSequencer will trigger simple lookups.
	if !(cfg.DNS().UseComplexLookup || semver.MustParse(ver.FinalizeVersion()).LT(semver.MustParse("2.25.0"))) {
		s.lookupSequencer = xsync.NewMap[string, clusterLookupResult]()
		go s.lookupSequencerGC()
	}

	rt := cfg.Routing()
	var err error
	s.alsoProxySubnets, err = validateSubnets("also-proxy", rt.AlsoProxy, s.alsoProxyVia)
	if err != nil {
		return nil, err
	}
	clog.Infof(s, "also-proxy subnets %v", s.alsoProxySubnets)

	s.neverProxySubnets, err = validateSubnets("never-proxy", rt.NeverProxy, nope)
	if err != nil {
		return nil, err
	}
	clog.Infof(s, "never-proxy subnets %v", s.neverProxySubnets)

	s.allowConflictingSubnets, err = validateSubnets("allow-conflicting", rt.AllowConflicting, nope)
	if err != nil {
		return nil, err
	}
	clog.Infof(s, "allow-conflicting subnets %v", s.allowConflictingSubnets)

	s.dnsServer = dns.NewServer(cfg.DNS(), s.Namespace, s.clusterLookup)
	s.SetTopLevelDomains(nil)

	// Set ourselves as the default dialer for the session.
	s.Context = tunnel.WithDialer(s.Context, s)

	// Terminate the routes watcher
	go func() {
		<-s.Done()
		close(s.routesCh)
	}()
	return s, nil
}

// lookupSequencerTTL is the maximum time to keep a lookup result cached with the purpose of avoiding
// both A and AAAA lookups for the same name.
const lookupSequencerTTL = 500 * time.Millisecond

// currentManagerConn is a grpc.ClientConnInterface that delegates every call
// to the session's current manager connection, so that a manager.ManagerClient
// captured at session start transparently follows a connection replaced by
// reconnectManager. Streams in flight on a replaced connection die with it;
// their retry loops pick up the current connection.
type currentManagerConn struct {
	s *session
}

func (c currentManagerConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return c.s.getManagerConn().Invoke(ctx, method, args, reply, opts...)
}

func (c currentManagerConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.s.getManagerConn().NewStream(ctx, desc, method, opts...)
}

func (s *session) getManagerConn() *grpc.ClientConn {
	s.managerConnMu.Lock()
	conn := s.managerConn
	s.managerConnMu.Unlock()
	return conn
}

func (s *session) managerClient() manager.ManagerClient {
	return manager.NewManagerClient(currentManagerConn{s: s})
}

// reconnectManager replaces the session's manager connection with one pinned to a
// freshly resolved manager pod. The old connection is closed unless it was borrowed
// from the user daemon.
func (s *session) reconnectManager() error {
	tc, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerConnect)
	defer cancel()
	conn, _, ver, err := s.ConnectToManager(tc, s.managerNamespace)
	if err != nil {
		return err
	}
	s.managerConnMu.Lock()
	old, owned := s.managerConn, s.ownsManagerConn
	s.managerConn = conn
	s.ownsManagerConn = true
	s.managerVersion = ver
	s.managerConnMu.Unlock()
	if old != nil && owned {
		_ = old.Close()
	}
	clog.Infof(s, "Reconnected to traffic-manager %s", ver)
	return nil
}

func (s *session) lookupSequencerGC() {
	// Cleans the lookupSequencer from time to time to avoid that it grows too big if many different
	// names are looked up.
	maps.GC(s.lookupSequencer, lookupSequencerTTL, s.Done(), func(key string, value clusterLookupResult) bool {
		return time.Since(value.created) > lookupSequencerTTL
	})
}

func (s *session) resolvePort(ctx context.Context, host, portStr string) (ap types.AddrPortProto, err error) {
	ix := strings.LastIndexByte(portStr, types.ProtoSeparator)
	proto := types.ProtoTCP
	if ix > 0 {
		proto, err = types.ParseProto(portStr[ix+1:])
		if err != nil {
			return ap, err
		}
		portStr = portStr[:ix]
	}

	if port, err := types.ParsePort(portStr); err == nil {
		ip, err := netip.ParseAddr(host)
		if err != nil {
			ip, err = dns.LookupIP(ctx, s.localDNS, dns2.Fqdn(host))
			if err != nil {
				return ap, err
			}
		}
		return types.AddrPortProto{AddrPort: netip.AddrPortFrom(ip, port), Proto: proto}, nil
	}

	// The toPort is symbolic, so it must be resolved using the Kubernetes API.
	_, err = netip.ParseAddr(host)
	if err == nil {
		return ap, errors.New("a symbolic port must be used with a service name, not an IP address")
	}
	return portforward.ResolveServiceAndPort(ctx, host, s.Namespace, portStr, proto)
}

func (s *session) rerouteRemotePort(ap types.AddrPortProto, newPort uint16) {
	if newPort != ap.Port() {
		clog.Debugf(s, "Rerouting %s via %d", ap, newPort)

		// Swap ports so that the port map reroutes requests for the new port to the original port.
		toPort := ap.Port()
		ap.AddrPort = netip.AddrPortFrom(ap.Addr(), newPort)
		if s.l4PortMap == nil {
			s.l4PortMap = xsync.NewMap[types.AddrPortProto, uint16]()
		}
		s.l4PortMap.Store(ap, toPort)
	}
}

func (s *session) addLocalClientRedirect(ap types.AddrPortProto, localPort uint16) {
	target := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), localPort)
	clog.Debugf(s, "Redirecting local client traffic for %s to %s", ap, target)
	if s.localClientRedirects == nil {
		s.localClientRedirects = xsync.NewMap[types.AddrPortProto, netip.AddrPort]()
	}
	s.localClientRedirects.Store(ap, target)
}

func (s *session) removeLocalClientRedirect(ap types.AddrPortProto) {
	clog.Debugf(s, "Removing local client redirect for %s", ap)
	if s.localClientRedirects != nil {
		s.localClientRedirects.Delete(ap)
	}
}

func (s *session) listLocalClientRedirects() []*rpc.LocalClientRedirect {
	if s.localClientRedirects == nil {
		return nil
	}
	redirects := make([]*rpc.LocalClientRedirect, 0, s.localClientRedirects.Size())
	s.localClientRedirects.Range(func(remote types.AddrPortProto, target netip.AddrPort) bool {
		hpb, err := remote.MarshalBinary()
		if err == nil {
			redirects = append(redirects, &rpc.LocalClientRedirect{
				DstHostPort: hpb,
				LocalPort:   uint32(target.Port()),
			})
		}
		return true
	})
	return redirects
}

type clusterLookupResult struct {
	created time.Time
	rrs     dnsproxy.RRs
	rCode   int
	err     error
}

// clusterLookup sends a Lookup or LookupDNS request to the traffic-manager and returns the result.
func (s *session) clusterLookup(ctx context.Context, q *dns2.Question) (dnsproxy.RRs, int, error) {
	clog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	if s.lookupSequencer == nil || !(q.Qtype == dns2.TypeA || q.Qtype == dns2.TypeAAAA) {
		return s.complexClusterLookup(ctx, q)
	}

	// The lookupSequencer ensures that successive calls for the same name, whether they are A or AAAA, are not
	// performed concurrently. The traffic manager will return all known IPs for the name regardless of the type.
	result, _ := s.lookupSequencer.Compute(q.Name, func(oldValue clusterLookupResult, loaded bool) (newValue clusterLookupResult, op xsync.ComputeOp) {
		if loaded && time.Since(oldValue.created) < lookupSequencerTTL {
			return oldValue, xsync.CancelOp
		}
		rrs, rCode, err := s.simpleLookup(ctx, q)
		return clusterLookupResult{
			created: time.Now(),
			rrs:     rrs,
			rCode:   rCode,
			err:     err,
		}, xsync.UpdateOp
	})
	return result.rrs, result.rCode, result.err
}

func (s *session) simpleLookup(ctx context.Context, question *dns2.Question) (dnsproxy.RRs, int, error) {
	var lookupClient interface {
		Lookup(context.Context, *manager.LookupRequest, ...grpc.CallOption) (*manager.LookupResponse, error)
	}
	request := &manager.LookupRequest{Session: s.session, Name: question.Name}
	if ags := s.agentClients; ags != nil {
		lookupClient = ags.GetRandomAgent(ctx)
	}
	if lookupClient == nil {
		clog.Debugf(ctx, "Using traffic-manager for lookup %q", question.Name)
		lookupClient = s.managerClient()
	} else {
		clog.Debugf(ctx, "Using traffic-agent for lookup %q", question.Name)
	}
	resp, err := lookupClient.Lookup(ctx, request)
	if status.Code(err) == codes.Unimplemented {
		return s.complexClusterLookup(ctx, question)
	}
	if err != nil {
		s.dnsFailures++
		rCode := rcodeFromError(err)
		clog.Errorf(ctx, "Lookup %q %s: %v", question.Name, dns2.RcodeToString[rCode], err)
		return nil, rCode, err
	}
	if len(resp.Ips) == 0 {
		return nil, dns2.RcodeNameError, nil
	}
	ips := make([]netip.Addr, len(resp.Ips))
	for i := range resp.Ips {
		_ = ips[i].UnmarshalBinary(resp.Ips[i])
	}
	if len(s.localTranslationSubnets) > 0 {
		for i, ip := range ips {
			ips[i], err = s.GetLocalIP(ip)
			if err != nil {
				return nil, dns2.RcodeServerFailure, err
			}
		}
	}
	ips4, ips6 := splitNameTypes(question.Name, ips)
	rrs := ensureBothFamilies(question.Name, ips4, ips6)
	rCode := dns2.RcodeSuccess
	return rrs, rCode, err
}

// rrHeader creates a common DNS RR header for INET class.
func rrHeader(name string, rrType uint16) dns2.RR_Header {
	return dns2.RR_Header{
		Name:   name,
		Rrtype: rrType,
		Class:  dns2.ClassINET,
	}
}

// splitNameTypes converts the binary-encoded IPs into A and AAAA resource records.
func splitNameTypes(name string, ips []netip.Addr) (dnsproxy.RRs, dnsproxy.RRs) {
	ips4 := make(dnsproxy.RRs, 0)
	ips6 := make(dnsproxy.RRs, 0)

	for _, addr := range ips {
		if addr.Is6() {
			ips6 = append(ips6, &dns2.AAAA{
				Hdr:  rrHeader(name, dns2.TypeAAAA),
				AAAA: addr.AsSlice(),
			})
		} else {
			ips4 = append(ips4, &dns2.A{
				Hdr: rrHeader(name, dns2.TypeA),
				A:   addr.AsSlice(),
			})
		}
	}
	return ips4, ips6
}

// ensureBothFamilies pads with an empty RR for the missing address family, preserving original behavior.
func ensureBothFamilies(name string, ips4, ips6 dnsproxy.RRs) dnsproxy.RRs {
	switch {
	case len(ips4) > 0 && len(ips6) == 0:
		ips6 = append(ips6, &dns2.AAAA{
			Hdr: rrHeader(name, dns2.TypeAAAA),
		})
	case len(ips6) > 0 && len(ips4) == 0:
		ips4 = append(ips4, &dns2.A{
			Hdr: rrHeader(name, dns2.TypeA),
		})
	}
	return append(ips4, ips6...)
}

// clusterLookup sends a LookupDNS request to the traffic-manager and returns the result.
func (s *session) complexClusterLookup(ctx context.Context, q *dns2.Question) (dnsproxy.RRs, int, error) {
	dnsResponse, err := s.managerClient().LookupDNS(ctx, &manager.DNSRequest{
		Session: s.session,
		Name:    q.Name,
		Type:    uint32(q.Qtype),
	})
	if err != nil {
		s.dnsFailures++
		rCode := rcodeFromError(err)
		clog.Errorf(ctx, "Lookup %s %q %s: %T %v", dns2.TypeToString[q.Qtype], q.Name, dns2.RcodeToString[rCode], err, err)
		return nil, rCode, err
	}
	answer, rCode, err := dnsproxy.FromRPC(dnsResponse)
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	if len(s.localTranslationSubnets) > 0 {
		for _, rr := range answer {
			switch rr := rr.(type) {
			case *dns2.A:
				var addr netip.Addr
				addr, err = s.GetLocalIP(netip.AddrFrom4([4]byte(rr.A)))
				if err == nil {
					rr.A = addr.AsSlice()
				}
			case *dns2.AAAA:
				var addr netip.Addr
				addr, err = s.GetLocalIP(netip.AddrFrom16([16]byte(rr.AAAA)))
				if err == nil {
					rr.AAAA = addr.AsSlice()
				}
			}
			if err != nil {
				rCode = dns2.RcodeServerFailure
				break
			}
		}
	}
	return answer, rCode, err
}

// rcodeFromError maps lookup errors to appropriate DNS RCODEs.
func rcodeFromError(err error) int {
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled),
		status.Code(err) == codes.DeadlineExceeded,
		status.Code(err) == codes.Canceled:
		return dns2.RcodeNameError
	default:
		return dns2.RcodeServerFailure
	}
}

func (s *session) GetLocalIP(destinationIP netip.Addr) (netip.Addr, error) {
	var err error
	va, _ := s.localTranslationTable.LoadOrCompute(destinationIP, func() (netip.Addr, bool) {
		for _, sn := range s.localTranslationSubnets {
			if sn.Contains(destinationIP) {
				var nip netip.Addr
				nip, err = s.nextVirtualIP(sn.workload, destinationIP)
				return nip, err != nil
			}
		}
		return netip.Addr{}, true
	})
	if err == nil && va.IsValid() {
		destinationIP = va
	}
	return destinationIP, err
}

func (s *session) nextVirtualIP(workload string, destinationIP netip.Addr) (netip.Addr, error) {
	va, err := s.vipGenerator.Next(destinationIP)
	if err != nil {
		return va, err
	}
	s.virtualIPs.Store(va, agentVIP{workload: workload, destinationIP: destinationIP})
	return va, nil
}

func (s *session) getNetworkConfig() *rpc.NetworkConfig {
	mc := client.GetConfig(s)
	r := mc.Routing()
	if s.tunVif != nil {
		r.Subnets = s.tunVif.Router.GetRoutedSubnets()
	} else {
		r.Subnets = nil
	}
	if len(s.effectiveNeverProxy) > 0 {
		r.NeverProxy = make([]netip.Prefix, len(s.effectiveNeverProxy))
		copy(r.NeverProxy, s.effectiveNeverProxy)
	} else {
		r.NeverProxy = nil
	}
	if len(s.alsoProxySubnets) > 0 {
		r.AlsoProxy = make([]netip.Prefix, len(s.alsoProxySubnets))
		copy(r.AlsoProxy, s.alsoProxySubnets)
	} else {
		r.AlsoProxy = nil
	}
	if len(s.allowConflictingSubnets) > 0 {
		r.AllowConflicting = make([]netip.Prefix, len(s.allowConflictingSubnets))
		copy(r.AllowConflicting, s.allowConflictingSubnets)
	} else {
		r.AllowConflicting = nil
	}
	d := mc.DNS()
	if proc.RunningInContainer() && s.teleroute != nil {
		las := s.teleroute.DaemonAddresses()
		d.LocalAddresses = make([]netip.AddrPort, len(las))
		for i, addr := range s.teleroute.DaemonAddresses() {
			d.LocalAddresses[i] = netip.AddrPortFrom(addr, 53)
		}
	} else {
		if s.localDNS.IsValid() {
			d.LocalAddresses = []netip.AddrPort{s.localDNS}
		} else {
			d.LocalAddresses = nil
		}
	}
	d.VIFAddress = s.vifDNS

	var portMappings []string
	if psz := s.l4PortMap.Size(); psz > 0 {
		portMappings = make([]string, 0, psz)
		s.l4PortMap.Range(func(key types.AddrPortProto, origPort uint16) bool {
			portMappings = append(portMappings, fmt.Sprintf("%s:%d", types.AddrPortProto{AddrPort: netip.AddrPortFrom(key.Addr(), origPort), Proto: key.Proto}, key.Port()))
			return true
		})
	}
	js, _ := json.Marshal(mc)
	return &rpc.NetworkConfig{
		Session:          s.session,
		PortMappings:     portMappings,
		ClientConfig:     js,
		MappedNamespaces: s.MappedNamespaces,
		ManagerNamespace: k8s.GetManagerNamespace(s),
	}
}

func (s *session) configureDNS(vifDNS netip.AddrPort, localDNS netip.AddrPort) {
	s.vifDNS = vifDNS
	s.localDNS = localDNS
}

// shouldProxySubnet returns true unless the given subnet is covered by a subnet in the neverProxySubnets list.
func (s *session) shouldProxySubnet(name string, sn netip.Prefix) bool {
	if sn.Addr().IsLoopback() {
		clog.Infof(s, "Will not proxy %s subnet %s, because it is loopback", name, sn)
		return false
	}
	for _, lt := range s.localTranslationSubnets {
		if subnet.Covers(lt.Prefix, sn) {
			clog.Infof(s, "Will not proxy %s subnet %s, because it covered by --proxy-via %s=%s", name, sn, lt.Prefix, lt.workload)
			return false
		}
	}
	for _, nps := range s.neverProxySubnets {
		if subnet.Covers(nps, sn) {
			// Allow if there's an also-proxy that is smaller, contradicting the never-proxy
			for _, aps := range s.alsoProxySubnets {
				if subnet.Covers(nps, aps) && subnet.Covers(aps, sn) {
					clog.Infof(s, "Will proxy %s subnet %s, because it is covered by also-proxy %s overriding never-proxy %s", name, sn, nps, aps)
					return true
				}
			}
			clog.Infof(s, "Will not proxy %s subnet %s, because it is covered by never-proxy %s", name, sn, nps)
			return false
		}
	}
	for _, npx := range s.subnetViaWorkloads {
		if name == "service" && npx.Subnet == "service" || name == "pod" && npx.Subnet == "pods" {
			clog.Infof(s, "Will not proxy %s subnet %s, because it is covered by --proxy-via %s=%s", name, sn, npx.Subnet, npx.Workload)
			return false
		}
	}
	return true
}

// networkReady returns a channel that is close when both the VIF and DNS are ready.
func (s *session) networkReady(ctx context.Context) <-chan error {
	rdy := make(chan error, 2)
	go func() {
		defer close(rdy)
		select {
		case <-ctx.Done():
			rdy <- ctx.Err()
		case err, ok := <-s.vifReady:
			if ok {
				rdy <- err
			} else {
				select {
				case <-ctx.Done():
				case <-s.dnsServer.Ready():
				}
			}
		}
	}()
	return rdy
}

func (s *session) watchClusterInfo(teleroutePort uint16) error {
	return watcher.WatchWithRetry(s, "WatchClusterInfo", client.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.ClusterInfo], error) {
			return s.managerClient().WatchClusterInfo(ctx, s.session)
		},
		func(mgrInfo *manager.ClusterInfo) error {
			if err := s.readAdditionalRouting(mgrInfo); err != nil {
				return err
			}
			select {
			case <-s.vifReady:
				if err := s.onClusterInfo(mgrInfo); err != nil {
					if !errors.Is(err, context.Canceled) {
						clog.Error(s, err)
					}
					return err
				}
			default:
				if err := s.onFirstClusterInfo(teleroutePort, mgrInfo); err != nil {
					if !errors.Is(err, context.Canceled) {
						clog.Error(s, err)
					}
					return err
				}
			}
			return nil
		},
		// The manager connection is pinned to one pod; when that pod goes away
		// the watch cannot recover on its own, so the repair establishes a
		// connection to a freshly resolved pod. The user daemon restores the
		// session itself, so a repair that races that restoration simply fails
		// and runs again on the next retry.
		s.reconnectManager,
	)
}

// createSubnetForDNSOnly will find a random IPv4 subnet that isn't currently routed and
// attach the DNS server to that subnet.
func (s *session) createSubnetForDNSOnly(mgrInfo *manager.ClusterInfo) {
	// Avoid alsoProxied and neverProxied
	avoid := make([]netip.Prefix, 0, len(s.alsoProxySubnets)+len(s.neverProxySubnets))
	avoid = append(avoid, s.alsoProxySubnets...)
	avoid = append(avoid, s.neverProxySubnets...)

	// Avoid the service subnets. They might be mapped with iptables (if running bare-metal) and
	// hence invisible when listing known routes.
	avoid = append(avoid, s.serviceSubnets...)

	// Avoid the pod subnets. They are probably visible as known routes, but we add them to
	// the avoid table to be sure.
	for _, ps := range mgrInfo.PodSubnets {
		avoid = append(avoid, iputil.RPCToPrefix(ps))
	}
	var err error
	if s.dnsServerSubnet, err = subnet.RandomIPv4Prefix(30, avoid); err != nil {
		clog.Error(s, err)
	}
}

func (s *session) onFirstClusterInfo(teleroutePort uint16, mgrInfo *manager.ClusterInfo) (err error) {
	defer func() {
		if err != nil {
			s.vifReady <- err
		}
		close(s.vifReady)
	}()
	if s.podDaemon {
		return nil
	}
	if teleroutePort > 0 {
		// Always proxy pods and services when using the teleroute network, so that we can inject synthetic IP:s as needed. This means
		// never relying on Docker's default route to reach the cluster.
		s.proxyClusterPods = true
		s.proxyClusterSvcs = true
	} else {
		s.proxyClusterPods = !s.hasPodConnectivity(mgrInfo)
		s.proxyClusterSvcs = !s.hasSvcConnectivity(mgrInfo)
	}
	return s.onClusterInfo(mgrInfo)
}

func (s *session) defaultRouteDNS(mgrInfo *manager.ClusterInfo, dnsAddr netip.Addr, subnets []netip.Prefix) (netip.Addr, []netip.Prefix, error) {
	// We'll need to synthesize a subnet where we can attach the DNS service when the VIF isn't configured
	// from cluster subnets. But not on darwin systems, because there the DNS is controlled by /etc/resolver
	// entries appointing the DNS service directly via localhost:<port>.
	if s.vipGenerator != nil {
		if !s.dnsServerSubnet.IsValid() {
			s.createSubnetForDNSOnly(mgrInfo)
		}
		clog.Infof(s, "Adding service subnet %s (for DNS only)", s.dnsServerSubnet)
		var wl string
		for _, snw := range s.subnetViaWorkloads {
			if sn, err := netip.ParsePrefix(snw.Subnet); err == nil && sn.Contains(dnsAddr) {
				wl = snw.Workload
				if wl == "local" {
					wl = ""
				}
			}
		}
		s.localTranslationSubnets = append(s.localTranslationSubnets, agentSubnet{
			Prefix:   s.dnsServerSubnet,
			workload: wl,
		})
		var err error
		dnsAddr, err = s.GetLocalIP(dnsAddr)
		if err != nil {
			return dnsAddr, subnets, err
		}
	} else {
		if !s.dnsServerSubnet.IsValid() {
			s.createSubnetForDNSOnly(mgrInfo)
		}
		clog.Infof(s, "Adding service subnet %s (for DNS only)", s.dnsServerSubnet)
		subnets = append(subnets, s.dnsServerSubnet)
		dnsIP := s.dnsServerSubnet.Addr().AsSlice()
		dnsIP[len(dnsIP)-1] = 2
		dnsAddr, _ = netip.AddrFromSlice(dnsIP)
	}
	return dnsAddr, subnets, nil
}

func (s *session) onClusterInfo(mgrInfo *manager.ClusterInfo) (err error) {
	if s.podDaemon {
		return nil
	}
	clog.Debugf(s, "WatchClusterInfo update")
	bwcompat.FixLegacyClusterInfo(mgrInfo)

	if mgrInfo.Routing == nil {
		mgrInfo.Routing = &manager.Routing{}
	}

	s.serviceSubnets = nil
	s.podSubnets = nil

	var subnets []netip.Prefix

	if s.proxyClusterSvcs {
		for _, sb := range mgrInfo.ServiceCidrs {
			var cidr netip.Prefix
			err = cidr.UnmarshalBinary(sb)
			if err != nil {
				return err
			}
			if s.shouldProxySubnet("service", cidr) {
				clog.Infof(s, "Adding service subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.serviceSubnets = append(s.serviceSubnets, cidr)
		}
	}

	if s.proxyClusterPods {
		for _, sn := range mgrInfo.PodSubnets {
			cidr := iputil.RPCToPrefix(sn)
			if s.shouldProxySubnet("pod", cidr) {
				clog.Infof(s, "Adding pod subnet %s", cidr)
				subnets = append(subnets, cidr)
			}
			s.podSubnets = append(s.podSubnets, cidr)
		}
	}

	if s.vipGenerator != nil {
		// Re-resolve the translated subnets now that pod and service subnets are
		// known, then make sure a generator exists for every family among them.
		s.consolidateProxyViaWorkloads()
		for _, sn := range s.localTranslationSubnets {
			s.vipGenerator.EnsureFamily(sn.Addr())
		}
		for _, vsn := range s.vipGenerator.Subnets() {
			subnets = append(subnets, vsn)
			clog.Debugf(s, "Adding VIP subnet %q to TUN-device", vsn)
		}
	}

	if !s.alsoProxyVia() {
		subnets = append(subnets, s.alsoProxySubnets...)
	}

	// We use the ManagerPodIp as the dnsIP. The reason for this is that no one should ever
	// talk to the traffic-manager directly using the TUN device, so it's safe to use its
	// IP to impersonate the DNS server. All traffic sent to that IP, will be routed to
	// the local DNS server.
	vifDNS, ok := netip.AddrFromSlice(mgrInfo.ManagerPodIp)
	if !ok {
		return fmt.Errorf("invalid traffic-manager pod ip address")
	}
	if s.vipGenerator != nil {
		vifDNS, err = s.GetLocalIP(vifDNS)
		if err != nil {
			return err
		}
	}
	dnsRouted := false
	if proc.RunningInContainer() {
		dnsRouted = true
	} else {
		for _, sn := range subnets {
			if sn.Contains(vifDNS) {
				dnsRouted = true
				break
			}
		}
	}
	if runtime.GOOS != "darwin" && !dnsRouted {
		vifDNS, subnets, err = s.defaultRouteDNS(mgrInfo, vifDNS, subnets)
		if err != nil {
			return err
		}
		dnsRouted = true
	}

	if dnsRouted {
		d := mgrInfo.Dns
		dnsAddress := netip.AddrPortFrom(vifDNS, 53)
		clog.Infof(s, "Setting client DNS to %s", vifDNS)
		clog.Infof(s, "Setting cluster domain to %q", d.ClusterDomain)
		s.dnsServer.SetClusterDNS(d, dnsAddress)
	}
	return s.reconcileSubnets(mgrInfo, subnets)
}

func (s *session) reconcileSubnets(mgrInfo *manager.ClusterInfo, subnets []netip.Prefix) error {
	if len(subnets) > 0 && s.tunVif == nil {
		var err error
		if s.tunVif, err = vif.NewTunnelingDevice(s, s.streamCreator()); err != nil {
			return fmt.Errorf("NewTunnelVIF: %w", err)
		}
	}

	neverProxySubnets, localDNSRoutes := s.neverProxyWithLocalDNS(subnets)
	proxy, neverProxy, neverProxyOverrides := computeNeverProxyOverrides(s, subnets, neverProxySubnets)
	s.effectiveNeverProxy = neverProxy
	if s.tunVif == nil {
		return nil
	}
	rt := s.tunVif.Router
	clog.Debugf(s, "allowConflicting is set to %v", s.allowConflictingSubnets)
	rt.UpdateWhitelist(s.allowConflictingSubnets)
	rt.SetLocalDNSRoutes(localDNSRoutes)

	err := rt.ValidateRoutes(s, proxy)
	if err != nil {
		if s.vipGenerator != nil {
			clog.Debugf(s, "vipGenerator is defined so error %s does not result in any translations", err)
			return err
		}
		if !client.GetConfig(s).Routing().AutoResolveConflicts {
			clog.Debugf(s, "autoResolveConflicts is false so %s isn't resolving itself", err)
			return err
		}
		// Check each subnet and add a translation for those that conflict.
		for _, pp := range proxy {
			if routeConflict := rt.ValidateRoutes(s, []netip.Prefix{pp}); routeConflict != nil {
				clog.Infof(s, "Translating IPs in conflicting subnet %s to the virtual subnet", pp)
				s.subnetViaWorkloads = append(s.subnetViaWorkloads, &rpc.SubnetViaWorkload{
					Subnet:   pp.String(),
					Workload: "local",
				})
			}
		}
		if aErr := s.activateProxyViaWorkloads(); aErr != nil {
			clog.Errorf(s, "activateProxyViaWorkloads: %v", aErr)
			return err
		}
		return s.onClusterInfo(mgrInfo)
	}

	clog.Debugf(s, "UpdatingRoutes %s, %s, %s", proxy, s.effectiveNeverProxy, neverProxyOverrides)
	err = rt.UpdateRoutes(s, proxy, s.effectiveNeverProxy, neverProxyOverrides)
	if err != nil {
		return err
	}
	sns := rt.GetRoutedSubnets()
	select {
	case <-s.Done():
	case s.routesCh <- sns:
	default:
	}
	return nil
}

func computeNeverProxyOverrides(ctx context.Context, subnets, nvp []netip.Prefix) (proxy, neverProxy, neverProxyOverrides []netip.Prefix) {
	neverProxy = slices.DeleteFunc(slices.Clone(nvp), func(nps netip.Prefix) bool {
		for _, ds := range subnets {
			if ds.Overlaps(nps) {
				return false
			}
		}
		// This never-proxy is pointless because it's not a subnet that we are routing
		clog.Infof(ctx, "Dropping never-proxy %q because it is not routed", nps)
		return true
	})

	proxy, neverProxyOverrides = subnet.Partition(subnets, func(i int, isn netip.Prefix) bool {
		for r, rsn := range subnets {
			if i == r {
				continue
			}
			if subnet.Covers(rsn, isn) && rsn != isn {
				for _, dsn := range neverProxy {
					if subnet.Covers(dsn, isn) {
						return false
					}
				}
			}
		}
		return true
	})
	return subnet.Unique(proxy), neverProxy, neverProxyOverrides
}

// neverProxyWithLocalDNS returns the configured never-proxy subnets extended with
// host routes for any local DNS server whose address is covered by one of the
// subnets that we are about to route. Without this, DNS queries sent to such a
// server would be captured by the TUN-device and tunnelled into the cluster
// instead of reaching the real resolver, breaking name resolution for everything
// that isn't a cluster name. See issue #2429.
func (s *session) neverProxyWithLocalDNS(subnets []netip.Prefix) (neverProxy, dnsRoutes []netip.Prefix) {
	cfg := client.GetConfig(s).DNS()

	// Collect the DNS server addresses from every source we know of: the user
	// configuration, the addresses the DNS server actually settled on at runtime
	// (e.g. resolved from /etc/resolv.conf), and the host's system resolvers.
	dnsServers := make([]netip.Addr, 0, len(cfg.LocalAddresses)+len(s.dnsServer.LocalAddresses))
	for _, ap := range cfg.LocalAddresses {
		dnsServers = append(dnsServers, ap.Addr())
	}
	for _, ap := range s.dnsServer.LocalAddresses {
		dnsServers = append(dnsServers, ap.Addr())
	}
	for _, ap := range dns.SystemResolvers(s) {
		dnsServers = append(dnsServers, ap.Addr())
	}

	return appendLocalDNSNeverProxy(s, s.neverProxySubnets, subnets, dnsServers)
}

// appendLocalDNSNeverProxy adds a host route (/32 or /128) to neverProxy for each
// DNS server address that is covered by one of the routed subnets and not already
// covered by a never-proxy entry. It also returns the host routes for those DNS
// servers (dnsRoutes), so the router can route just these via their real path
// while leaving every other never-proxy entry on the default route.
func appendLocalDNSNeverProxy(ctx context.Context, neverProxy, subnets []netip.Prefix, dnsServers []netip.Addr) (allNeverProxy, dnsRoutes []netip.Prefix) {
	nvp := slices.Clone(neverProxy)
	for _, dnsIP := range slice.AppendUnique([]netip.Addr{}, dnsServers...) {
		if !dnsIP.IsValid() || dnsIP.IsLoopback() || dnsIP.IsUnspecified() {
			continue
		}
		for _, sn := range subnets {
			if !sn.Contains(dnsIP) {
				continue
			}
			hostRoute := netip.PrefixFrom(dnsIP, dnsIP.BitLen())
			dnsRoutes = append(dnsRoutes, hostRoute)
			if !slices.Contains(nvp, hostRoute) {
				clog.Infof(ctx, "Adding local DNS server %s to never-proxy because it is covered by routed subnet %s", dnsIP, sn)
				nvp = append(nvp, hostRoute)
			}
			break
		}
	}
	return subnet.Unique(nvp), dnsRoutes
}

func validateSubnets(name string, ns []netip.Prefix, allowLoopback func() bool) ([]netip.Prefix, error) {
	ns = subnet.Unique(ns)
	rs := make([]netip.Prefix, 0, len(ns))
	for _, sn := range ns {
		if sn.Addr().IsLoopback() && !allowLoopback() {
			return nil, fmt.Errorf(`%s subnet %s is a loopback subnet. It is never proxied`, name, sn)
		}
		rs = append(rs, sn)
	}
	return subnet.Unique(rs), nil
}

// alsoProxyVia will return true when the connection was made using --subnet-via all=<workload> or --subnet-via also=<workload>.
func (s *session) alsoProxyVia() bool {
	for _, pvx := range s.subnetViaWorkloads {
		if pvx.Subnet == "also" { // no need to test for "all". It's normalized into ["also", "pods", "service"]
			return true
		}
	}
	return false
}

func (s *session) readAdditionalRouting(mgrInfo *manager.ClusterInfo) error {
	if r := mgrInfo.Routing; r != nil {
		sns, err := validateSubnets("also-proxy", iputil.RPCsToPrefixes(r.AlsoProxySubnets), s.alsoProxyVia)
		if err != nil {
			return err
		}
		s.alsoProxySubnets = subnet.Unique(append(s.alsoProxySubnets, sns...))
		clog.Infof(s, "also-proxy subnets %v", s.alsoProxySubnets)

		sns, err = validateSubnets("never-proxy", iputil.RPCsToPrefixes(r.NeverProxySubnets), nope)
		if err != nil {
			return err
		}
		s.neverProxySubnets = subnet.Unique(append(s.neverProxySubnets, sns...))
		clog.Infof(s, "never-proxy subnets %v", s.neverProxySubnets)

		sns, err = validateSubnets("allow-conflicting", iputil.RPCsToPrefixes(r.AllowConflictingSubnets), nope)
		if err != nil {
			return err
		}
		s.allowConflictingSubnets = subnet.Unique(append(s.allowConflictingSubnets, sns...))
		clog.Infof(s, "allow-conflicting subnets %v", s.allowConflictingSubnets)
	}
	return nil
}

// hasSvcConnectivity checks the connectivity of the agent-injector server. It returns true if it can be connected, false otherwise.
func (s *session) hasSvcConnectivity(info *manager.ClusterInfo) bool {
	// The traffic-manager service is headless, which means we can't try a GRPC connection to its ClusterIP.
	// Instead, we try an HTTP health check on the agent-injector server, since that one does expose a ClusterIP.
	// This is less precise than if we could check for our own GRPC, since /healthz is a common enough health check path,
	// but hopefully, the server on the other end isn't configured to respond to the hostname "agent-injector" if it isn't the agent-injector.
	if info.InjectorSvcIp == nil {
		clog.Debugf(s, "No injector service IP given; usually this is because the traffic-manager is older than the telepresence binary."+
			"Connectivity check for services set to pass.")
		return false
	}
	ct := client.GetConfig(s).Timeouts().Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		clog.Info(s, "Connectivity check for services disabled")
		return false
	}
	ip := net.IP(info.InjectorSvcIp).String()
	port := info.InjectorSvcPort
	if port == 0 {
		port = 8443
	}
	tr := &http.Transport{
		// Skip checking the cert because its trust chain is loaded into a secret on the cluster; we'd fail to verify it
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	hcl := &http.Client{Transport: tr}
	tCtx, tCancel := context.WithTimeout(s, ct)
	defer tCancel()
	url := iputil.JoinHostPort(ip, uint16(port))
	url = fmt.Sprintf("https://%s/healthz", url)
	request, err := http.NewRequestWithContext(tCtx, http.MethodHead, url, nil)
	if err != nil {
		// As far as I can tell, this error means a) that the context was cancelled before the request could be allocated, or b) that the request is misconstructed, e.g. bad method.
		// Neither of those two should really happen here (unless you set the timeout to a few microseconds, maybe), but we can't really continue. May as well route the cluster.
		clog.Errorf(s, "Unexpected: service conn check could not build request: %v. Will route services anyway.", err)
		return false
	}
	request.Header.Set("Host", info.InjectorSvcHost)
	clog.Debugf(s, "Performing service connectivity check on %s with Host %s and timeout %s", url, info.InjectorSvcHost, ct)
	resp, err := hcl.Do(request)
	if err != nil {
		// This means either network errors (timeouts, failed to connect), or that the server doesn't speak HTTP.
		clog.Debugf(s, "Will proxy services (%v)", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		clog.Warnf(s, "service IP %s is connectable, but did not respond as expected (status code %d)."+
			" Will proxy services, but this may interfere with your VPN routes.", info.InjectorSvcIp, resp.StatusCode)
		return false
	}
	clog.Info(s, "Already connected to cluster, will not map service subnets.")
	return true
}

// managerIPInPodSubnets reports whether the manager's pod IP is covered by one
// of the announced pod subnets. A traffic-manager that runs on the host network
// reports a node address as its pod IP, and such an address cannot serve as a
// probe target for pod connectivity.
func managerIPInPodSubnets(info *manager.ClusterInfo) bool {
	ip, ok := netip.AddrFromSlice(info.ManagerPodIp)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	for _, sn := range info.PodSubnets {
		if iputil.RPCToPrefix(sn).Contains(ip) {
			return true
		}
	}
	return false
}

// hasPodConnectivity verifies connectivity to the traffic-manager in the cluster and returns true if connectivity is successful; false otherwise.
func (s *session) hasPodConnectivity(info *manager.ClusterInfo) bool {
	if info.ManagerPodIp == nil {
		return false
	}
	ct := client.GetConfig(s).Timeouts().Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		clog.Info(s, "Connectivity check for pods disabled")
		return false
	}
	if !managerIPInPodSubnets(info) {
		clog.Infof(s, "Will proxy pods. Manager pod IP %s is not within a pod subnet, so it cannot be used for a connectivity check",
			net.IP(info.ManagerPodIp))
		return false
	}
	ip := net.IP(info.ManagerPodIp).String()
	port := info.ManagerPodPort
	if port == 0 {
		port = 8081 // Traffic managers before 2.8.0 didn't include the port because it was hardcoded at 8081
	}
	tCtx, tCancel := context.WithTimeout(s, ct)
	defer tCancel()
	clog.Debugf(s, "Performing pod connectivity check on IP %s with timeout %s", ip, ct)
	conn, err := grpc.NewClient(
		net.JoinHostPort(ip, strconv.Itoa(int(port))),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		clog.Debugf(s, "Will proxy pods. NewClient: %v", err)
		return false
	}
	state := conn.GetState()
	conn.Connect() // Initiate the connection
	defer conn.Close()

	// Wait for the connection to reach READY state or fail
	for {
		switch state {
		case connectivity.Ready:
			// Connection is established
			_, err = manager.NewManagerClient(conn).Version(tCtx, &empty.Empty{})
			if err != nil {
				if s.Err() != nil {
					return false // session cancelled
				}
				clog.Warnf(s, "Manager IP %s is connectable but not a traffic-manager instance (%v)."+
					" Will proxy pods, but this may interfere with your VPN routes.", ip, err)
				return false
			}
			clog.Info(s, "Will not proxy pods. Already connected to cluster.")
			return true
		case connectivity.TransientFailure, connectivity.Shutdown:
			clog.Debugf(s, "Will proxy pods, connection failed: state=%v", state)
			return false
		default:
			if !conn.WaitForStateChange(tCtx, state) {
				// Normal. The context timed out before the connection reached READY state.
				clog.Debug(s, "Will proxy pods")
				return false
			}
			state = conn.GetState()
		}
	}
}

func (s *session) run(initErrs chan<- error) {
	defer func() {
		clog.Info(s, "-- session ended")
	}()
	g := log.NewGroup(s)
	if err := s.Start(g, 0); err != nil {
		defer close(initErrs)
		initErrs <- err
		return
	}
	close(initErrs)
	err := g.Wait()
	if err != nil {
		clog.Errorf(s, "session ended with error: %v", err)
	}
}

func (s *session) Start(g log.Group, teleroutePort uint16) error {
	clusterCfg := client.GetConfig(s).Cluster()
	if clusterCfg.AgentPortForward {
		// Relay mode: the user daemon has already computed the namespace set (the
		// same one it requests from the traffic-manager) and pushes deltas via
		// WatchAgentPods instead of this daemon watching the traffic-manager itself.
		relayMode := len(s.agentPodNamespaces) > 0
		var agentNamespaces []string
		if relayMode {
			agentNamespaces = s.agentPodNamespaces
		} else {
			agentNamespaces = slices.DeleteFunc(s.GetCurrentNamespaces(true), func(ns string) bool {
				return !k8s.CanPortForward(s, ns)
			})
		}
		if len(agentNamespaces) > 0 {
			s.agentClients = agentpf.NewClients(s.Cluster, s.session, agentNamespaces, s.sessionCredentialToken)
			// Receive a callback per dial accepted from the dial watchers,
			// so we can report incoming-dial counters to the user daemon
			// at session end (see daemon.Activity).
			s.agentClients.SetDialMetrics(s)
			// Flush the DNS cache whenever the agent set changes. A workload may have been recreated
			// with a new Service ClusterIP, which would otherwise be masked by a stale cache entry.
			s.agentClients.SetChangeListener(s.dnsServer.Flush)
			// Agent connections go through the same forwarder as the manager-bound QUIC
			// tunnel, just with a different SNI per agent, so they should dial the same
			// candidate address startQuicTunnel's own probe already found reachable
			// (nil/"" until that probe resolves, in which case quicEndpointFor falls
			// back to the descriptor's own host/port -- see its doc).
			s.agentClients.SetPreferredQuicAddr(func() string {
				if c := s.quicConn.Load(); c != nil {
					return c.RemoteAddr().String()
				}
				return ""
			})
			if relayMode {
				g.Go("agentPods", func(ctx context.Context) error {
					return s.agentClients.RunDeltaSink(s.managerClient())
				})
			} else {
				g.Go("agentPods", func(ctx context.Context) error {
					return s.agentClients.WatchAgentPods(s.managerClient())
				})
			}
		} else {
			clog.Infof(s, "Agent port-forwards are disabled. Client is not permitted to do port-forward to any mapped namespace")
		}
	}
	if err := s.activateProxyViaWorkloads(); err != nil {
		return err
	}
	if s.podDaemon {
		return nil
	}

	cancelDNSLock := sync.Mutex{}
	cancelDNS := func() {}

	g.Go("network", func(ctx context.Context) error {
		defer func() {
			cancelDNSLock.Lock()
			cancelDNS()
			cancelDNSLock.Unlock()
		}()
		return s.watchClusterInfo(teleroutePort)
	})

	// Opportunistically dial the traffic-manager's QUIC tunnel endpoint. This must never
	// fail or delay session startup, so it runs detached from the rest of startup and
	// always reports success to the group; startQuicTunnel itself never returns an error.
	g.Go("quic", func(ctx context.Context) error {
		s.startQuicTunnel(ctx)
		return nil
	})

	// Retries the QUIC dial after a later trip to fallback (e.g. a manager restart that
	// rotates the QUIC CA); see quicReprobeLoop. Runs for the life of the session,
	// mostly idle: it blocks on quicReprobeTrigger until onQuicFallback wakes it.
	g.Go("quic-reprobe", func(ctx context.Context) error {
		s.quicReprobeLoop(ctx)
		return nil
	})

	if s.agentClients == nil && len(s.subnetViaWorkloads) > 0 {
		return fmt.Errorf("--proxy-via can only be used when cluster.agentPortForward is enabled")
	}

	// At this point, we wait until the VIF is ready. It will be shortly after
	// the first ClusterInfo is received from the traffic-manager. A timeout
	// is needed so that we don't wait forever on a traffic-manager that has
	// been terminated for some reason.
	wc, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerConnect)
	defer cancel()
	select {
	case <-wc.Done():
		// Time out when waiting for the cluster info to arrive
		s.vifReady <- wc.Err()
		s.dnsServer.Stop()
		return wc.Err()
	case err := <-s.vifReady:
		if err != nil {
			s.dnsServer.Stop()
			return err
		}
	}

	// Start the router and the DNS service and wait for the context
	// to be done. Then shut things down in order. The following happens:
	// 1. The DNS worker terminates (it needs the TUN device to be alive while doing that)
	// 2. The TUN device is closed (by the stop method). This unblocks the routerWorker's pending read on the device.
	// 3. The routerWorker terminates.
	g.Go("dns", func(ctx context.Context) error {
		defer s.stop()
		cancelDNSLock.Lock()
		ctx, cancelDNS = context.WithCancel(ctx)
		cancelDNSLock.Unlock()
		var dev vif.Device
		if s.tunVif != nil {
			dev = s.tunVif.Device
		}
		return s.dnsServer.Worker(ctx, dev, s.configureDNS)
	})

	if s.tunVif != nil {
		g.Go("vif", s.tunVif.Run)
		err := s.waitForProxyViaWorkloads()
		if err != nil {
			return err
		}
		if teleroutePort > 0 {
			s.teleroute, err = teleroute.StartServer(g, s.tunVif, s.routesCh, teleroutePort)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) stop() {
	if !atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		// session already stopped (or is stopping)
		return
	}
	clog.Debug(s, "Bringing down TUN-device")
	cc, cancel := context.WithTimeout(context.WithoutCancel(s), time.Second)
	go func() {
		s.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&s.closing, 2)

	if conn := s.getManagerConn(); conn != nil {
		clog.Debug(s, "Closing port-forward to traffic-manager")
		// Avoid sporadic hang when the client connection is torn down.
		cc, cancel = context.WithTimeout(context.WithoutCancel(s), time.Second)
		go func() {
			_ = conn.Close()
			cancel()
		}()
		<-cc.Done()
	}

	if conn := s.quicConn.Load(); conn != nil {
		clog.Debug(s, "Closing QUIC tunnel connection to traffic-manager")
		clog.Infof(s, "QUIC tunnel datagram counters: %s", s.datagramCounters)
		_ = conn.CloseWithError(0, "session closed")
	}

	if s.tunVif != nil {
		cc, cancel = context.WithTimeout(context.WithoutCancel(s), time.Second)
		defer cancel()
		if err := s.tunVif.Close(cc); err != nil {
			clog.Errorf(s, "unable to close %s: %v", s.tunVif.Device.Name(), err)
		}
	}
}

func (s *session) activateProxyViaWorkloads() error {
	sl := len(s.subnetViaWorkloads)
	if sl == 0 {
		return nil
	}
	wlNames := s.consolidateProxyViaWorkloads()

	// Virtual IPs are drawn per address family: from the configured VirtualSubnet
	// for its own family, and from the platform default (IPv4) or the fixed
	// Telepresence ULA (IPv6) for the other. This honors a user-configured IPv6
	// VirtualSubnet while still giving a dual-stack cluster both families.
	// EnsureFamily creates the per-family generator only for the families that
	// actually appear among the translated subnets.
	v4Subnet := client.DefaultVirtualSubnet()
	v6Subnet := vif.TelepresenceULA6
	if vs := client.GetConfig(s).Routing().VirtualSubnet; vs.IsValid() {
		if vs.Addr().Is6() {
			v6Subnet = vs
		} else {
			v4Subnet = vs
		}
	}
	s.vipGenerator = vip.NewGenerators(v4Subnet, v6Subnet)
	for _, sn := range s.localTranslationSubnets {
		s.vipGenerator.EnsureFamily(sn.Addr())
	}
	clog.Debugf(s, "ProxyVIA using subnets %v", s.vipGenerator.Subnets())

	for _, wlName := range wlNames {
		if s.agentClients == nil {
			return errcat.User.Newf("Agent port-forwards are disabled. Client is not permitted to do proxy-via %s", wlName)
		}
		clog.Debugf(s, "Ensuring proxy-via agent in %s", wlName)
		_, err := s.managerClient().EnsureAgent(s, &manager.EnsureAgentRequest{
			Session: s.session,
			Name:    wlName,
		})
		if err != nil {
			if st, ok := status.FromError(err); ok {
				if st.Code() == codes.FailedPrecondition {
					return errcat.User.New(st.Message())
				}
			}
			return err
		}
	}
	return nil
}

func (s *session) consolidateProxyViaWorkloads() []string {
	desiredVips := make(map[string][]netip.Prefix)
	snCount := 0
	for _, pvx := range s.subnetViaWorkloads {
		switch pvx.Subnet {
		case "also":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.alsoProxySubnets...)
			snCount += len(s.alsoProxySubnets)
		case "pods":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.podSubnets...)
			snCount += len(s.podSubnets)
		case "service":
			desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], s.serviceSubnets...)
			snCount += len(s.serviceSubnets)
		default:
			sn, err := netip.ParsePrefix(pvx.Subnet)
			if err != nil {
				clog.Warnf(s, "unable to parse proxy-via subnet %s", pvx.Subnet)
			} else {
				desiredVips[pvx.Workload] = append(desiredVips[pvx.Workload], sn)
				snCount++
			}
		}
	}

	wlNames := make([]string, 0, len(desiredVips))
	lcs := make([]agentSubnet, 0, snCount)
	for wlName, sns := range desiredVips {
		if wlName == "local" {
			wlName = ""
		} else {
			wlNames = append(wlNames, wlName)
		}
		for _, sn := range sns {
			lcs = append(lcs, agentSubnet{Prefix: sn, workload: wlName})
		}
	}
	s.localTranslationSubnets = lcs
	clog.Debugf(s, "Local translation subnets: %v", s.localTranslationSubnets)
	return wlNames
}

func (s *session) waitForProxyViaWorkloads() error {
	wc := len(s.subnetViaWorkloads)
	if wc == 0 {
		return nil
	}
	to := client.GetConfig(s).Timeouts().Get(client.TimeoutIntercept)
	waitCh := make(chan error)

	// Need unique workload names
	ws := make([]string, 0, len(s.subnetViaWorkloads))
	for _, svw := range s.subnetViaWorkloads {
		if svw.Workload != "local" {
			ws = slice.AppendUnique(ws, svw.Workload)
		}
	}
	for _, wl := range ws {
		s.agentClients.SetProxyVia(wl)
		clog.Debugf(s, "Waiting for proxy-via agent in %s", wl)
		go func(wl string) {
			waitCh <- s.agentClients.WaitForWorkload(to, wl)
		}(wl)
	}
	for _, wl := range ws {
		select {
		case <-s.Done():
			return nil
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("proxy-via agent in %s failed: %w", wl, err)
			}
			clog.Debugf(s, "Wait succeeded for proxy-via agent in %s", wl)
		}
	}
	return nil
}

func (s *session) SetTopLevelDomains(topLevelDomains []string) {
	s.dnsServer.SetTopLevelDomainsAndSearchPath(s, topLevelDomains, s.Namespace)
}

func (s *session) SetExcludes(excludes []string) {
	s.dnsServer.SetExcludes(excludes)
}

func (s *session) SetMappings(mappings []*rpc.DNSMapping) {
	s.dnsServer.SetMappings(mappings)
}

func (s *session) translateEnvIPs(environment *rpc.Environment) *rpc.Environment {
	vip.TranslateEnvironmentIPs(s, environment.Env, s)
	return environment
}

func (s *session) lookupIP(rq *rpc.LookupIPRequest) (*rpc.LookupIPResponse, error) {
	ip, err := dns.LookupIP(s, s.localDNS, rq.Name)
	if err != nil {
		return nil, err
	}
	rsp := new(rpc.LookupIPResponse)
	rsp.Ip, _ = ip.MarshalBinary()
	return rsp, nil
}

func (s *session) MapsIPv4() bool {
	for _, p := range s.localTranslationSubnets {
		if p.Addr().Is4() {
			return true
		}
	}
	return false
}

func (s *session) MapsIPv6() bool {
	for _, p := range s.localTranslationSubnets {
		if p.Addr().Is6() {
			return true
		}
	}
	return false
}

func (s *session) waitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest) (*rpc.WaitForAgentIPResponse, error) {
	if s.agentClients == nil {
		return nil, status.Error(codes.Unavailable, "")
	}
	ip, ok := netip.AddrFromSlice(request.Ip)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "")
	}
	namespace := request.Namespace
	if namespace == "" {
		namespace = s.Namespace
	}
	err := s.agentClients.WaitForIP(ctx, request.Timeout.AsDuration(), namespace, ip)
	if err != nil {
		return nil, grpcErrors.FromError(err, codes.Internal, err.Error())
	}
	ip, err = s.GetLocalIP(ip)
	if err != nil {
		return nil, err
	}
	return &rpc.WaitForAgentIPResponse{LocalIp: ip.AsSlice()}, nil
}

// applyAgentPodsDelta forwards a relayed agent-pod delta, pushed by the user daemon's
// WatchAgentPods call, to the agent-pod client set. s.agentClients is nil whenever this
// daemon isn't running an agent-pod watch at all (AgentPortForward disabled, or no
// port-forwardable namespace); the caller must treat the resulting error as a reason to
// stop relaying.
func (s *session) applyAgentPodsDelta(delta *rpc.AgentPodsDelta) error {
	if s.agentClients == nil {
		return status.Error(codes.FailedPrecondition, "no agent-pod client set for this session")
	}
	return s.agentClients.ApplyPodsDelta(delta.Reset_, delta.Upserts, delta.Removals)
}

func (s *session) ManagerVersion() semver.Version {
	s.managerConnMu.Lock()
	ver := s.managerVersion
	s.managerConnMu.Unlock()
	return ver
}

func (s *session) DialTCP(ctx context.Context, addr netip.AddrPort) (conn net.Conn, err error) {
	var d tunnel.Dialer
	if s.tunVif != nil && s.tunVif.Router.Routes(addr.Addr()) {
		d = s.tunVif
	} else {
		d = tunnel.DefaultDialer{}
	}
	return d.DialTCP(ctx, addr)
}

func (s *session) DialUDP(ctx context.Context, localAddr netip.AddrPort, remoteAddr netip.AddrPort) (conn net.Conn, err error) {
	var d tunnel.Dialer
	if s.tunVif != nil && s.tunVif.Router.Routes(remoteAddr.Addr()) {
		d = s.tunVif
	} else {
		d = tunnel.DefaultDialer{}
	}
	return d.DialUDP(ctx, localAddr, remoteAddr)
}

func (s *session) MarkActivity() {
	select {
	case s.activity <- time.Now():
	default:
	}
}

// IncomingDial is the tunnel.DialMetrics callback for each dial request
// successfully read by the agentpf dial watchers.
func (s *session) IncomingDial() {
	s.incomingDials.Add(1)
}

// IncomingDialError is the tunnel.DialMetrics callback for each accepted
// dial that failed to produce a usable tunnel.
func (s *session) IncomingDialError() {
	s.incomingDialErrors.Add(1)
}

func (s *service) ActivityWatcher(_ *empty.Empty, stream grpc.ServerStreamingServer[rpc.Activity]) error {
	// Pin the session for the lifetime of the stream: a Disconnect may nil
	// out s.session while this stream is still being served.
	s.sessionLock.RLock()
	session := s.session
	s.sessionLock.RUnlock()
	if session == nil {
		return nil
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-session.Done():
			// Emit one terminal Activity carrying the session totals so
			// the user daemon can report them before the session goes
			// away. Best-effort: a send failure here just means the
			// client already gave up listening.
			_ = stream.Send(session.sessionEndActivity())
			return nil
		case at := <-s.activity:
			err := stream.Send(&rpc.Activity{Activity: timestamppb.New(at)})
			if err != nil {
				return err
			}
		}
	}
}

// sessionEndActivity returns the final Activity message for this session,
// carrying the session duration and the telemetry counters accumulated
// since session start.
func (s *session) sessionEndActivity() *rpc.Activity {
	now := time.Now()
	return &rpc.Activity{
		Activity:             timestamppb.New(now),
		SessionEnd:           true,
		SessionDuration:      durationpb.New(now.Sub(s.sessionStart)),
		OutboundTunnels:      s.outboundTunnels.Load(),
		OutboundTunnelErrors: s.outboundTunnelErrors.Load(),
		IncomingDials:        s.incomingDials.Load(),
		IncomingDialErrors:   s.incomingDialErrors.Load(),
	}
}
