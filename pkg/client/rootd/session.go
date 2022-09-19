package rootd

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	dns2 "github.com/miekg/dns"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/routing"
)

// session resolves DNS names and routes outbound traffic that is centered around a TUN device. The router is
// similar to a TUN-to-SOCKS5 but uses a bidirectional gRPC muxTunnel instead of SOCKS when communicating with the
// traffic-manager. The addresses of the device are derived from IP addresses sent to it from the user
// daemon (which in turn receives them from the cluster).
//
// Data sent to the device is received as L3 IP-packets and parsed into L4 UDP and TCP before they
// are dispatched over the muxTunnel. Returned payloads are wrapped as IP-packets before written
// back to the device.
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
// Negotiation:
//
// UDP is of course very simple. It's fire and forget. There's no negotiation whatsoever.
//
// TCP requires a complete workflow engine on the TUN-device side (see tcp.Handler). All TCP negotiation,
// takes place in the client and the same bidirectional muxTunnel is then used to send both TCP and UDP
// packets to the manager. TCP will send some control packets. One to verify that a connection can
// be established at the manager side, and one when the connection is closed (from either side).
//
// A zero session is invalid; you must use newSession.
type session struct {
	scout *scout.Reporter

	// dev is the TUN device that gets configured with the subnets found in the cluster
	dev vif.Device

	stack *stack.Stack

	// clientConn is the connection that uses the connector's socket
	clientConn *grpc.ClientConn

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient manager.ManagerClient

	// managerVersion is the version of the connected traffic-manager
	managerVersion semver.Version

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *tunnel.Pool

	// fragmentMap is when concatenating ipv4 fragments
	fragmentMap map[uint16][]*buffer.Data

	// The local dns server
	dnsServer *dns.Server

	// remoteDnsIP is the IP of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd-resolved. The current macOS and the overriding solution
	// will dispatch directly to the local DNS service without going through the TUN device but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	remoteDnsIP net.IP

	// dnsLocalAddr is address of the local DNS service.
	dnsLocalAddr *net.UDPAddr

	// Cluster subnets reported by the traffic-manager
	clusterSubnets []*net.IPNet

	// Subnets configured by the user
	alsoProxySubnets []*net.IPNet

	// Subnets configured not to be proxied
	neverProxyRoutes []*routing.Route
	// Subnets that the router is currently configured with. Managed, and only used in
	// the refreshSubnets() method.
	curSubnets      []*net.IPNet
	curStaticRoutes []*routing.Route

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

	// Whether pods and services should be proxied by the TUN-device
	proxyCluster bool
}

// connectToManager connects to the traffic-manager and asserts that its version is compatible
func connectToManager(c context.Context) (*grpc.ClientConn, manager.ManagerClient, semver.Version, error) {
	// First check. Establish connection
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts
	tc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err := client.DialSocket(tc, client.ConnectorSocketName,
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	var mgrVer semver.Version
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The connector called us, and then it died which means we will die too. This is
			// a race, but it's not an error.
			return nil, nil, mgrVer, nil
		}
		return nil, nil, mgrVer, client.CheckTimeout(tc, err)
	}

	mc := manager.NewManagerClient(conn)
	ver, err := mc.Version(c, &empty.Empty{})
	if err != nil {
		conn.Close()
		return nil, nil, mgrVer, fmt.Errorf("failed to retrieve manager version: %w", err)
	}

	verStr := strings.TrimPrefix(ver.Version, "v")
	dlog.Infof(c, "Connected to Manager %s", verStr)
	mgrVer, err = semver.Parse(verStr)
	if err != nil {
		conn.Close()
		return nil, nil, mgrVer, fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}

	if mgrVer.LE(semver.MustParse("2.4.4")) {
		conn.Close()
		return nil, nil, mgrVer, errcat.User.Newf("unsupported traffic-manager version %s. Minimum supported version is 2.4.5", mgrVer)
	}
	return conn, mc, mgrVer, nil
}

func convertSubnets(c context.Context, ms []*manager.IPNet) []*net.IPNet {
	ns := make([]*net.IPNet, len(ms))
	for i, m := range ms {
		n := iputil.IPNetFromRPC(m)
		dlog.Infof(c, "Adding also-proxy subnet %s", n)
		ns[i] = n
	}
	return ns
}

// newSession returns a new properly initialized session object.
func newSession(c context.Context, scout *scout.Reporter, mi *rpc.OutboundInfo) (*session, error) {
	dlog.Info(c, "-- Starting new session")
	conn, mc, ver, err := connectToManager(c)
	if mc == nil || err != nil {
		return nil, err
	}

	s := &session{
		scout:            scout,
		handlers:         tunnel.NewPool(),
		fragmentMap:      make(map[uint16][]*buffer.Data),
		rndSource:        rand.NewSource(time.Now().UnixNano()),
		session:          mi.Session,
		managerClient:    mc,
		managerVersion:   ver,
		clientConn:       conn,
		alsoProxySubnets: convertSubnets(c, mi.AlsoProxySubnets),
		neverProxyRoutes: routing.Routes(c, convertSubnets(c, mi.NeverProxySubnets)),
		proxyCluster:     true,
	}

	s.dev, err = vif.OpenTun(c, &s.closing)
	if err != nil {
		return nil, err
	}

	if dnsproxy.ManagerCanDoDNSQueryTypes(ver) {
		s.dnsServer = dns.NewServer(mi.Dns, s.clusterLookup, false)
	} else {
		s.dnsServer = dns.NewServer(mi.Dns, s.legacyClusterLookup, true)
	}
	return s, nil
}

// clusterLookup sends a LookupDNS request to the traffic-manager and returns the result
func (s *session) clusterLookup(ctx context.Context, q *dns2.Question) ([]dns2.RR, int, error) {
	dlog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	r, err := s.managerClient.LookupDNS(ctx, &manager.DNSRequest{
		Session: s.session,
		Name:    q.Name,
		Type:    uint32(q.Qtype),
	})
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	return dnsproxy.FromRPC(r)
}

// clusterLookup sends a LookupHost request to the traffic-manager and returns the result
func (s *session) legacyClusterLookup(ctx context.Context, q *dns2.Question) ([]dns2.RR, int, error) {
	qType := q.Qtype
	if !(qType == dns2.TypeA || qType == dns2.TypeAAAA) {
		return nil, dns2.RcodeNotImplemented, nil
	}
	dlog.Debugf(ctx, "Lookup %s %q", dns2.TypeToString[q.Qtype], q.Name)
	s.dnsLookups++

	r, err := s.managerClient.LookupHost(ctx, &manager.LookupHostRequest{
		Session: s.session,
		Name:    q.Name[:len(q.Name)-1],
	})
	if err != nil {
		s.dnsFailures++
		return nil, dns2.RcodeServerFailure, err
	}
	ips := iputil.IPsFromBytesSlice(r.Ips)
	if len(ips) == 0 {
		return nil, dns2.RcodeNameError, nil
	}
	rrHeader := func() dns2.RR_Header {
		return dns2.RR_Header{Name: q.Name, Rrtype: qType, Class: dns2.ClassINET, Ttl: 4}
	}
	var rrs []dns2.RR
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			rrs = append(rrs, &dns2.A{
				Hdr: rrHeader(),
				A:   ip4,
			})
		}
	}
	return rrs, dns2.RcodeSuccess, nil
}

func (s *session) getInfo() *rpc.OutboundInfo {
	info := rpc.OutboundInfo{
		Session: s.session,
		Dns:     s.dnsServer.GetConfig(),
	}
	if s.dnsLocalAddr != nil {
		info.Dns.RemoteIp = s.dnsLocalAddr.IP
	}
	if len(s.alsoProxySubnets) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(s.alsoProxySubnets))
		for i, ap := range s.alsoProxySubnets {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC(ap)
		}
	}

	if len(s.neverProxyRoutes) > 0 {
		info.NeverProxySubnets = make([]*manager.IPNet, len(s.neverProxyRoutes))
		for i, np := range s.neverProxyRoutes {
			info.NeverProxySubnets[i] = iputil.IPNetToRPC(np.RoutedNet)
		}
	}

	return &info
}

func (s *session) configureDNS(dnsIP net.IP, dnsLocalAddr *net.UDPAddr) {
	s.remoteDnsIP = dnsIP
	s.dnsLocalAddr = dnsLocalAddr
}

func (s *session) reconcileStaticRoutes(ctx context.Context) (err error) {
	desired := []*routing.Route{}
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "reconcileStaticRoutes")
	defer tracing.EndAndRecord(span, err)

	// We're not going to add static routes unless they're actually needed
	// (i.e. unless the existing CIDRs overlap with the never-proxy subnets)
	for _, r := range s.neverProxyRoutes {
		for _, s := range s.curSubnets {
			if s.Contains(r.RoutedNet.IP) || r.Routes(s.IP) {
				desired = append(desired, r)
				break
			}
		}
	}

adding:
	for _, r := range desired {
		for _, c := range s.curStaticRoutes {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue adding
			}
		}
		if err := r.AddStatic(ctx); err != nil {
			dlog.Errorf(ctx, "failed to add static route %s: %v", r, err)
		}
	}

removing:
	for _, c := range s.curStaticRoutes {
		for _, r := range desired {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue removing
			}
		}
		if err := c.RemoveStatic(ctx); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", c, err)
		}
	}
	s.curStaticRoutes = desired

	return nil
}

func (s *session) refreshSubnets(ctx context.Context) (err error) {
	// Create a unique slice of all desired subnets.
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "refreshSubnets")
	defer tracing.EndAndRecord(span, err)
	desired := make([]*net.IPNet, len(s.clusterSubnets)+len(s.alsoProxySubnets))
	copy(desired, s.clusterSubnets)
	copy(desired[len(s.clusterSubnets):], s.alsoProxySubnets)
	desired = subnet.Unique(desired)

	// Remove all no longer desired subnets from the t.curSubnets
	var removed []*net.IPNet
	s.curSubnets, removed = subnet.Partition(s.curSubnets, func(_ int, sn *net.IPNet) bool {
		for _, d := range desired {
			if subnet.Equal(sn, d) {
				return true
			}
		}
		return false
	})

	// Remove already routed subnets from the desiredSubnets
	added, _ := subnet.Partition(desired, func(_ int, sn *net.IPNet) bool {
		for _, d := range s.curSubnets {
			if subnet.Equal(sn, d) {
				return false
			}
		}
		return true
	})

	// Add desiredSubnets to the currently routed subnets
	s.curSubnets = append(s.curSubnets, added...)

	for _, sn := range removed {
		if err := s.dev.RemoveSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}

	for _, sn := range added {
		if err := s.dev.AddSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
		}
	}

	return s.reconcileStaticRoutes(ctx)
}

func (s *session) watchClusterInfo(ctx context.Context, cfgComplete chan<- struct{}) {
	backoff := 100 * time.Millisecond

	for ctx.Err() == nil {
		infoStream, err := s.managerClient.WatchClusterInfo(ctx, s.session)
		if err != nil {
			err = fmt.Errorf("error when calling WatchClusterInfo: %w", err)
			dlog.Warn(ctx, err)
		}

		for err == nil && ctx.Err() == nil {
			mgrInfo, err := infoStream.Recv()
			if err != nil {
				if gErr, ok := status.FromError(err); ok {
					switch gErr.Code() {
					case codes.Canceled:
						// The connector, which is routing this connection, cancelled it, which means that the client
						// session is dead.
						return
					case codes.Unavailable:
						// Abrupt shutdown. This is nothing that the session should survive
						dlog.Errorf(ctx, "WatchClusterInfo recv: Unavailable: %v", gErr.Message())
					}
				} else {
					dlog.Errorf(ctx, "WatchClusterInfo recv: %v", err)
				}
				break
			}
			ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "ClusterInfoUpdate")
			if cfgComplete != nil {
				s.checkConnectivity(ctx, mgrInfo)
				if ctx.Err() != nil {
					span.End()
					return
				}
				dns := mgrInfo.Dns
				if dns == nil {
					// Older traffic-manager. Use deprecated mgrInfo fields for DNS
					dns = &manager.DNS{
						KubeIp:        mgrInfo.KubeDnsIp,
						ClusterDomain: mgrInfo.ClusterDomain,
					}
				}
				remoteIp := net.IP(dns.KubeIp)
				dlog.Infof(ctx, "Setting cluster DNS to %s", remoteIp)
				dlog.Infof(ctx, "Setting cluster domain to %q", dns.ClusterDomain)
				s.dnsServer.SetClusterDNS(dns)
				s.stack, err = vif.NewStack(ctx, s.dev, s.streamCreator())
				if err != nil {
					dlog.Errorf(ctx, "NewStack: %v", err)
					return
				}

				if r := mgrInfo.Routing; r != nil {
					s.alsoProxySubnets = subnet.Unique(append(s.alsoProxySubnets, convertSubnets(ctx, r.AlsoProxySubnets)...))
					nps := subnet.Unique(append(routing.Subnets(s.neverProxyRoutes), convertSubnets(ctx, r.NeverProxySubnets)...))
					s.neverProxyRoutes = routing.Routes(ctx, nps)
				}

				close(cfgComplete)
				cfgComplete = nil
				span.SetAttributes(
					attribute.Bool("tel2.proxy-cluster", s.proxyCluster),
					attribute.Bool("tel2.cfg-complete", false),
					attribute.Stringer("tel2.cluster-dns", remoteIp),
					attribute.String("tel2.cluster-domain", dns.ClusterDomain),
				)
			} else {
				span.SetAttributes(
					attribute.Bool("cfgComplete", false),
				)
			}
			s.onClusterInfo(ctx, mgrInfo)
			span.End()
		}
		dtime.SleepWithContext(ctx, backoff)
		backoff *= 2
		if backoff > 15*time.Second {
			backoff = 15 * time.Second
		}
	}
}

func (s *session) onClusterInfo(ctx context.Context, mgrInfo *manager.ClusterInfo) {
	dlog.Debugf(ctx, "WatchClusterInfo update")

	subnets := make([]*net.IPNet, 0, 1+len(mgrInfo.PodSubnets))
	if s.proxyCluster {
		if mgrInfo.ServiceSubnet != nil {
			cidr := iputil.IPNetFromRPC(mgrInfo.ServiceSubnet)
			dlog.Infof(ctx, "Adding service subnet %s", cidr)
			subnets = append(subnets, cidr)
		}

		for _, sn := range mgrInfo.PodSubnets {
			cidr := iputil.IPNetFromRPC(sn)
			dlog.Infof(ctx, "Adding pod subnet %s", cidr)
			subnets = append(subnets, cidr)
		}
	}

	s.clusterSubnets = subnets
	if err := s.refreshSubnets(ctx); err != nil {
		dlog.Error(ctx, err)
	}
}

func (s *session) checkConnectivity(ctx context.Context, info *manager.ClusterInfo) {
	if info.ManagerPodIp == nil {
		return
	}
	ct := client.GetConfig(ctx).Timeouts.Get(client.TimeoutConnectivityCheck)
	if ct == 0 {
		dlog.Debug(ctx, "Connectivity check disabled")
		return
	}
	ip := net.IP(info.ManagerPodIp).String()
	tCtx, tCancel := context.WithTimeout(ctx, ct)
	defer tCancel()
	dlog.Debugf(ctx, "Performing connectivity check with timeout %s", ct)
	conn, err := grpc.DialContext(tCtx, fmt.Sprintf("%s:8081", ip), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		dlog.Debugf(ctx, "Will proxy pods (%v)", err)
		return
	}
	conn.Close()
	s.proxyCluster = false
	dlog.Info(ctx, "Already connected to cluster, will not map cluster subnets")
}

func (s *session) run(c context.Context) error {
	defer dlog.Info(c, "-- Session ended")

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	cancelDNSLock := sync.Mutex{}
	cancelDNS := func() {}

	cfgComplete := make(chan struct{})
	g.Go("watch-cluster-info", func(ctx context.Context) error {
		defer func() {
			cancelDNSLock.Lock()
			cancelDNS()
			cancelDNSLock.Unlock()
		}()
		s.watchClusterInfo(ctx, cfgComplete)
		return nil
	})

	select {
	case <-c.Done():
		return nil
	case <-cfgComplete:
	}

	// Start the router and the DNS service and wait for the context
	// to be done. Then shut things down in order. The following happens:
	// 1. The DNS worker terminates (it needs the TUN device to be alive while doing that)
	// 2. The TUN device is closed (by the stop method). This unblocks the routerWorker's pending read on the device.
	// 3. The routerWorker terminates.
	g.Go("dns", func(ctx context.Context) error {
		defer s.stop(c) // using group parent context
		cancelDNSLock.Lock()
		ctx, cancelDNS = context.WithCancel(ctx)
		cancelDNSLock.Unlock()
		return s.dnsServer.Worker(ctx, s.dev, s.configureDNS)
	})
	g.Go("stack", func(_ context.Context) error {
		s.stack.Wait()
		return nil
	})
	return g.Wait()
}

func (s *session) stop(c context.Context) {
	if !atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		// Session already stopped (or is stopping)
		return
	}
	dlog.Debug(c, "Bringing down TUN-device")

	s.scout.Report(c, "incluster_dns_queries",
		scout.Entry{Key: "total", Value: s.dnsLookups},
		scout.Entry{Key: "failures", Value: s.dnsFailures})

	cc, cancel := context.WithTimeout(c, time.Second)
	defer cancel()
	go func() {
		s.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&s.closing, 2)

	s.stack.Close()

	cc = dcontext.WithoutCancel(c)
	for _, np := range s.curStaticRoutes {
		err := np.RemoveStatic(cc)
		if err != nil {
			dlog.Warnf(c, "error removing route %s: %v", np, err)
		}
	}
	if err := s.dev.Close(); err != nil {
		dlog.Errorf(c, "unable to close %s: %v", s.dev.Name(), err)
	}

	dlog.Debug(c, "Sending disconnect message to connector")
	_, _ = connector.NewConnectorClient(s.clientConn).Disconnect(c, &empty.Empty{})
	s.clientConn.Close()
	dlog.Debug(c, "Connector disconnect complete")
}

func (s *session) SetSearchPath(ctx context.Context, paths []string, namespaces []string) {
	s.dnsServer.SetSearchPath(ctx, paths, namespaces)
}
