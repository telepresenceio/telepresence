package rootd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"

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
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
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
	cancel context.CancelFunc

	scout *scout.Reporter

	// dev is the TUN device that gets configured with the subnets found in the cluster
	dev *vif.Device

	// clientConn is the connection that uses the connector's socket
	clientConn *grpc.ClientConn

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient manager.ManagerClient

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
	neverProxySubnets []routing.Route
	// Subnets that the router is currently configured with. Managed, and only used in
	// the refreshSubnets() method.
	curSubnets      []*net.IPNet
	curStaticRoutes []routing.Route

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
func connectToManager(c context.Context) (*grpc.ClientConn, manager.ManagerClient, error) {
	// First check. Establish connection
	clientConfig := client.GetConfig(c)
	tos := &clientConfig.Timeouts
	tc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err := client.DialSocket(tc, client.ConnectorSocketName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The connector called us, and then it died which means we will die too. This is
			// a race, but it's not an error.
			return nil, nil, nil
		}
		return nil, nil, client.CheckTimeout(tc, err)
	}

	mc := manager.NewManagerClient(conn)
	ver, err := mc.Version(c, &empty.Empty{})
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("failed to retrieve manager version: %w", err)
	}

	verStr := strings.TrimPrefix(ver.Version, "v")
	dlog.Infof(c, "Connected to Manager %s", verStr)
	mgrVer, err := semver.Parse(verStr)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}

	if mgrVer.LE(semver.MustParse("2.4.4")) {
		conn.Close()
		return nil, nil, errcat.User.Newf("unsupported traffic-manager version %s. Minimum supported version is 2.4.5", mgrVer)
	}
	return conn, mc, nil
}

func convertAlsoProxySubnets(c context.Context, ms []*manager.IPNet) []*net.IPNet {
	ns := make([]*net.IPNet, len(ms))
	for i, m := range ms {
		n := iputil.IPNetFromRPC(m)
		dlog.Infof(c, "Adding also-proxy subnet %s", n)
		ns[i] = n
	}
	return ns
}

func convertNeverProxySubnets(c context.Context, ms []*manager.IPNet) []routing.Route {
	rs := make([]routing.Route, 0, len(ms))
	for _, m := range ms {
		n := iputil.IPNetFromRPC(m)
		r, err := routing.GetRoute(c, n)
		if err != nil {
			dlog.Errorf(c, "unable to get route for never-proxied subnet %s. "+
				"If this is your kubernetes API server you may want to open an issue, since telepresence may "+
				"not work if it falls within the CIDR for pods/services. Error: %v",
				n, err)
			continue
		}
		dlog.Infof(c, "Adding never-proxy subnet %s", n)
		rs = append(rs, r)
	}
	return rs
}

// newSession returns a new properly initialized session object.
func newSession(c context.Context, scout *scout.Reporter, mi *rpc.OutboundInfo) (*session, error) {
	conn, mc, err := connectToManager(c)
	if mc == nil || err != nil {
		return nil, err
	}

	dev, err := vif.OpenTun(c)
	if err != nil {
		return nil, err
	}

	s := &session{
		cancel:            func() {},
		scout:             scout,
		dev:               dev,
		handlers:          tunnel.NewPool(),
		fragmentMap:       make(map[uint16][]*buffer.Data),
		rndSource:         rand.NewSource(time.Now().UnixNano()),
		session:           mi.Session,
		managerClient:     mc,
		clientConn:        conn,
		alsoProxySubnets:  convertAlsoProxySubnets(c, mi.AlsoProxySubnets),
		neverProxySubnets: convertNeverProxySubnets(c, mi.NeverProxySubnets),
		proxyCluster:      true,
	}
	s.dnsServer = dns.NewServer(mi.Dns, s.clusterLookup)
	return s, nil
}

// clusterLookup sends a LookupHost request to the traffic-manager and returns the result
func (s *session) clusterLookup(ctx context.Context, key string) ([][]byte, error) {
	dlog.Debugf(ctx, "LookupHost %q", key)
	s.dnsLookups++
	r, err := s.managerClient.LookupHost(ctx, &manager.LookupHostRequest{
		Session: s.session,
		Host:    key,
	})
	if err != nil || len(r.Ips) == 0 {
		s.dnsFailures++
	}
	if err != nil {
		return nil, err
	}
	return r.Ips, nil
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

	if len(s.neverProxySubnets) > 0 {
		info.NeverProxySubnets = make([]*manager.IPNet, len(s.neverProxySubnets))
		for i, np := range s.neverProxySubnets {
			info.NeverProxySubnets[i] = iputil.IPNetToRPC(np.RoutedNet)
		}
	}

	return &info
}

func (s *session) configureDNS(dnsIP net.IP, dnsLocalAddr *net.UDPAddr) {
	s.remoteDnsIP = dnsIP
	s.dnsLocalAddr = dnsLocalAddr
}

func (s *session) reconcileStaticRoutes(ctx context.Context) error {
	desired := []routing.Route{}

	// We're not going to add static routes unless they're actually needed
	// (i.e. unless the existing CIDRs overlap with the never-proxy subnets)
	for _, r := range s.neverProxySubnets {
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
		if err := s.dev.AddStaticRoute(ctx, r); err != nil {
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
		if err := s.dev.RemoveStaticRoute(ctx, c); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", c, err)
		}
	}
	s.curStaticRoutes = desired

	return nil
}

func (s *session) refreshSubnets(ctx context.Context) error {
	// Create a unique slice of all desired subnets.
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
				if ctx.Err() == nil && !errors.Is(err, io.EOF) {
					dlog.Errorf(ctx, "WatchClusterInfo recv: %v", err)
				}
				break
			}
			if cfgComplete != nil {
				s.checkConnectivity(ctx, mgrInfo)
				if ctx.Err() != nil {
					return
				}
				remoteIp := net.IP(mgrInfo.KubeDnsIp)
				dlog.Infof(ctx, "Setting cluster DNS to %s", remoteIp)
				dlog.Infof(ctx, "Setting cluster domain to %q", mgrInfo.ClusterDomain)
				s.dnsServer.SetClusterDomainAndDNS(mgrInfo.ClusterDomain, remoteIp)

				close(cfgComplete)
				cfgComplete = nil
			}
			s.onClusterInfo(ctx, mgrInfo)
		}
		dtime.SleepWithContext(ctx, backoff)
		backoff *= 2
		if backoff > 3*time.Second {
			backoff = 3 * time.Second
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
	ip := net.IP(info.ManagerPodIp).String()
	tCtx, tCancel := context.WithTimeout(ctx, 2*time.Second)
	defer tCancel()
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
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	cfgComplete := make(chan struct{})
	g.Go("watch-cluster-info", func(ctx context.Context) error {
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
		defer s.stop(c)
		return s.dnsServer.Worker(ctx, s.dev, s.configureDNS)
	})
	g.Go("router", s.routerWorker)
	return g.Wait()
}

func (s *session) stop(c context.Context) {
	dlog.Debug(c, "Brining down TUN-device")

	s.scout.Report(c, "incluster_dns_queries",
		scout.Entry{Key: "total", Value: s.dnsLookups},
		scout.Entry{Key: "failures", Value: s.dnsFailures})

	atomic.StoreInt32(&s.closing, 1)
	cc, cancel := context.WithTimeout(c, time.Second)
	defer cancel()
	go func() {
		s.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&s.closing, 2)

	cc = dcontext.WithoutCancel(c)
	for _, np := range s.curStaticRoutes {
		err := s.dev.RemoveStaticRoute(cc, np)
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
