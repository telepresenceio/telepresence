package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	"golang.org/x/net/ipv4"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/icmp"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/tcp"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/udp"
)

// session resolves DNS names and routes outbound traffic that is centered around a TUN device. It's similar to a
// TUN-to-SOCKS5 but uses a bidirectional gRPC muxTunnel instead of SOCKS when communicating with the
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
	// Namespaces, accessible using <service-name>.<namespace-name>
	namespaces map[string]struct{}
	domains    map[string]struct{}
	search     []string

	// The domainsLock locks usage of namespaces, domains, and search
	domainsLock sync.RWMutex

	searchPathCh chan []string

	// Local DNS cache.
	dnsCache sync.Map

	dnsConfig *rpc.DNSConfig

	scout chan<- scout.ScoutReport
	// dev is the TUN device that gets configured with the subnets found in the cluster
	dev *vif.Device

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient manager.ManagerClient

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *tunnel.Pool

	// fragmentMap is when concatenating ipv4 fragments
	fragmentMap map[uint16][]*buffer.Data

	// dnsIP is the IP of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd-resolved. The current macOS and the overriding solution
	// will dispatch directly to the local DNS service without going through the TUN device but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	dnsIP   net.IP
	dnsPort uint16

	// dnsLocalAddr is the address of the local DNS server
	dnsLocalAddr *net.UDPAddr

	// clusterDomain reported by the traffic-manager
	clusterDomain string

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

	// cfgComplete will be closed as soon as the connector has sent over the correct port to
	// the traffic manager and the managerClient has been connected.
	cfgComplete chan struct{}

	// tmVerOk will be closed as soon as the correct tunnel version has been negotiated with
	// the traffic manager
	tmVerOk chan struct{}

	// rndSource is the source for the random number generator in the TCP handlers
	rndSource rand.Source
}

func newLocalUDPListener(c context.Context) (net.PacketConn, error) {
	lc := &net.ListenConfig{}
	return lc.ListenPacket(c, "udp", "127.0.0.1:0")
}

// newSession returns a new properly initialized session object.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
func newSession(c context.Context, dnsIPStr string, scout chan<- scout.ScoutReport) (*session, error) {
	// seed random generator (used when shuffling IPs)
	rand.Seed(time.Now().UnixNano())

	td, err := vif.OpenTun(c)
	if err != nil {
		return nil, err
	}
	ret := &session{
		dnsConfig: &rpc.DNSConfig{
			LocalIp: iputil.Parse(dnsIPStr),
		},
		namespaces:   make(map[string]struct{}),
		domains:      make(map[string]struct{}),
		search:       []string{""},
		searchPathCh: make(chan []string, 5),
		scout:        scout,
		dev:          td,
		handlers:     tunnel.NewPool(),
		cfgComplete:  make(chan struct{}),
		tmVerOk:      make(chan struct{}),
		fragmentMap:  make(map[uint16][]*buffer.Data),
		rndSource:    rand.NewSource(time.Now().UnixNano()),
	}
	return ret, nil
}

// tel2SubDomain aims to fix a search-path problem when using Docker on non-linux systems where
// Docker uses its own search-path for single label names. This means that the search path that
// is declared in the macOS resolver is ignored although the rest of the DNS-resolution works OK.
// Since the search-path is likely to change during a session, a stable fake domain is needed to
// emulate the search-path. That fake-domain can then be used in the search path declared in the
// Docker config.
//
// The "tel2-search" domain fills this purpose and a request for "<single label name>.tel2-search."
// will be resolved as "<single label name>." using the search path of this resolver.
const tel2SubDomain = "tel2-search"
const tel2SubDomainDot = tel2SubDomain + "."

var localhostIPs = []net.IP{{127, 0, 0, 1}, {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}

func (s *session) shouldDoClusterLookup(query string) bool {
	if strings.HasSuffix(query, "."+s.clusterDomain) && strings.Count(query, ".") < 4 {
		return false
	}

	query = query[:len(query)-1] // skip last dot

	// Always include configured includeSuffixes
	for _, sfx := range s.dnsConfig.IncludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return true
		}
	}

	// Skip configured excludeSuffixes
	for _, sfx := range s.dnsConfig.ExcludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return false
		}
	}
	return true
}

func (s *session) resolveInCluster(c context.Context, query string) (results []net.IP) {
	query = strings.ToLower(query)
	query = strings.TrimSuffix(query, tel2SubDomainDot)

	if query == "localhost." {
		// BUG(lukeshu): I have no idea why a lookup
		// for localhost even makes it to here on my
		// home WiFi when connecting to a k3sctl
		// cluster (but not a kubernaut.io cluster).
		// But it does, so I need this in order to be
		// productive at home.  We should really
		// root-cause this, because it's weird.
		return localhostIPs
	}

	if !s.shouldDoClusterLookup(query) {
		return nil
	}
	// Don't report queries that won't be resolved in-cluster, since that'll report every single DNS query on the user's machine
	defer func() {
		r := scout.ScoutReport{
			Action: "incluster_dns_query",
			Metadata: map[string]interface{}{
				"had_results": results != nil,
			},
		}
		// Post to scout channel but never block if it's full
		select {
		case s.scout <- r:
		default:
		}
	}()

	// Give the cluster lookup a reasonable timeout.
	c, cancel := context.WithTimeout(c, s.dnsConfig.LookupTimeout.AsDuration())
	defer cancel()

	queryWithNoTrailingDot := query[:len(query)-1]
	dlog.Debugf(c, "LookupHost %q", queryWithNoTrailingDot)
	response, err := s.managerClient.LookupHost(c, &manager.LookupHostRequest{
		Session: s.session,
		Host:    queryWithNoTrailingDot,
	})
	if err != nil {
		dlog.Error(c, client.CheckTimeout(c, err))
		return nil
	}
	if len(response.Ips) == 0 {
		return nil
	}
	ips := make(iputil.IPs, len(response.Ips))
	for i, ip := range response.Ips {
		ips[i] = ip
	}
	return ips
}

func (s *session) setInfo(ctx context.Context, info *rpc.OutboundInfo) error {
	if info.Dns == nil {
		info.Dns = &rpc.DNSConfig{}
	}
	if oldIP := s.dnsConfig.GetLocalIp(); len(oldIP) > 0 {
		info.Dns.LocalIp = oldIP
	}
	if len(info.Dns.ExcludeSuffixes) == 0 {
		info.Dns.ExcludeSuffixes = []string{
			".arpa",
			".com",
			".io",
			".net",
			".org",
			".ru",
		}
	}
	if info.Dns.LookupTimeout.AsDuration() <= 0 {
		info.Dns.LookupTimeout = durationpb.New(4 * time.Second)
	}
	s.dnsConfig = info.Dns
	return s.setOutboundInfo(ctx, info)
}

func (s *session) getInfo() *rpc.OutboundInfo {
	info := rpc.OutboundInfo{
		Dns: &rpc.DNSConfig{
			RemoteIp: s.dnsIP,
		},
	}
	if s.dnsConfig != nil {
		info.Dns.LocalIp = s.dnsConfig.LocalIp
		info.Dns.ExcludeSuffixes = s.dnsConfig.ExcludeSuffixes
		info.Dns.IncludeSuffixes = s.dnsConfig.IncludeSuffixes
		info.Dns.LookupTimeout = s.dnsConfig.LookupTimeout
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

// SetSearchPath updates the DNS search path used by the resolver
func (s *session) setSearchPath(ctx context.Context, paths, namespaces []string) {
	// Provide direct access to intercepted namespaces
	for _, ns := range namespaces {
		paths = append(paths, ns+".svc."+s.clusterDomain)
	}
	select {
	case <-ctx.Done():
	case s.searchPathCh <- paths:
	}
}

func (s *session) processSearchPaths(g *dgroup.Group, processor func(context.Context, []string) error) {
	g.Go("SearchPaths", func(c context.Context) error {
		var prevPaths []string
		unchanged := func(paths []string) bool {
			if len(paths) != len(prevPaths) {
				return false
			}
			for i, path := range paths {
				if path != prevPaths[i] {
					return false
				}
			}
			return true
		}

		for {
			select {
			case <-c.Done():
				return nil
			case paths := <-s.searchPathCh:
				if len(s.searchPathCh) > 0 {
					// Only interested in the last one
					continue
				}
				if !unchanged(paths) {
					dlog.Debugf(c, "%v -> %v", prevPaths, paths)
					prevPaths = make([]string, len(paths))
					copy(prevPaths, paths)
					if err := processor(c, paths); err != nil {
						return err
					}
				}
			}
		}
	})
}

func (s *session) flushDNS() {
	s.dnsCache.Range(func(key, _ interface{}) bool {
		s.dnsCache.Delete(key)
		return true
	})
}

// splitToUDPAddr splits the given address into an UDPAddr. It's
// an  error if the address is based on a hostname rather than an IP.
func splitToUDPAddr(netAddr net.Addr) (*net.UDPAddr, error) {
	ip, port, err := iputil.SplitToIPPort(netAddr)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}
func (s *session) configured() <-chan struct{} {
	return s.tmVerOk
}

func (s *session) configureDNS(_ context.Context, dnsLocalAddr *net.UDPAddr) {
	s.dnsPort = 53
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

func (s *session) setOutboundInfo(ctx context.Context, mi *daemon.OutboundInfo) (err error) {
	if s.managerClient == nil {
		// First check. Establish connection
		clientConfig := client.GetConfig(ctx)
		tos := &clientConfig.Timeouts
		tc, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
		defer cancel()

		var conn *grpc.ClientConn

		conn, err = client.DialSocket(tc, client.ConnectorSocketName)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// The connector called us, and then it died which means we will die too. This is
				// a race, but it's not an error.
				return nil
			}
			return client.CheckTimeout(tc, err)
		}
		s.session = mi.Session
		s.managerClient = manager.NewManagerClient(conn)

		if len(mi.AlsoProxySubnets) > 0 {
			s.alsoProxySubnets = make([]*net.IPNet, len(mi.AlsoProxySubnets))
			for i, ap := range mi.AlsoProxySubnets {
				apSn := iputil.IPNetFromRPC(ap)
				dlog.Infof(ctx, "Adding also-proxy subnet %s", apSn)
				s.alsoProxySubnets[i] = apSn
			}
		}
		if len(mi.NeverProxySubnets) > 0 {
			s.neverProxySubnets = []routing.Route{}
			for _, np := range mi.NeverProxySubnets {
				npSn := iputil.IPNetFromRPC(np)
				dlog.Infof(ctx, "Adding never-proxy subnet %s", npSn)
				route, err := routing.GetRoute(ctx, npSn)
				if err != nil {
					dlog.Errorf(ctx, "unable to get route for never-proxied subnet %s. "+
						"If this is your kubernetes API server you may want to open an issue, since telepresence may not work if it falls within the CIDR for pods/services. "+
						"Error: %v",
						iputil.IPNetFromRPC(np), err)
					continue
				}
				s.neverProxySubnets = append(s.neverProxySubnets, route)
			}
		}
		s.dnsIP = mi.Dns.RemoteIp

		dgroup.ParentGroup(ctx).Go("watch-cluster-info", func(ctx context.Context) error {
			s.watchClusterInfo(ctx)
			return nil
		})
	}
	return nil
}

func (s *session) watchClusterInfo(ctx context.Context) {
	cfgComplete := s.cfgComplete
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
			dlog.Debugf(ctx, "WatchClusterInfo update")

			subnets := make([]*net.IPNet, 0, 1+len(mgrInfo.PodSubnets))
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

			s.clusterSubnets = subnets
			if err := s.refreshSubnets(ctx); err != nil {
				dlog.Error(ctx, err)
			}

			if cfgComplete != nil {
				// Only set clusterDNS when it hasn't been explicitly set with the --dns option
				if s.dnsIP == nil {
					dlog.Infof(ctx, "Setting cluster DNS to %s", net.IP(mgrInfo.KubeDnsIp))
					s.dnsIP = mgrInfo.KubeDnsIp
				}
				dlog.Infof(ctx, "Setting cluster domain to %q", mgrInfo.ClusterDomain)
				s.clusterDomain = mgrInfo.ClusterDomain
				if s.clusterDomain == "" {
					// Traffic manager predates 2.4.3 and doesn't report a cluster domain. Only thing
					// left to do then is to assume it's the standard one.
					s.clusterDomain = "cluster.local."
				}
				close(cfgComplete)
				cfgComplete = nil
			}
		}
		dtime.SleepWithContext(ctx, backoff)
		backoff *= 2
		if backoff > 3*time.Second {
			backoff = 3 * time.Second
		}
	}
}

func (s *session) stop(c context.Context) {
	if atomic.CompareAndSwapInt32(&s.closing, 0, 1) {
		cc, cancel := context.WithTimeout(c, time.Second)
		defer cancel()
		go func() {
			atomic.StoreInt32(&s.closing, 1)
			s.handlers.CloseAll(cc)
			cancel()
		}()
		<-cc.Done()
	}
	if atomic.CompareAndSwapInt32(&s.closing, 1, 2) {
		for _, np := range s.curStaticRoutes {
			err := s.dev.RemoveStaticRoute(c, np)
			if err != nil {
				dlog.Warnf(c, "error removing route %s: %v", np, err)
			}
		}
		s.dev.Close()
	}
}

var blockedUDPPorts = map[uint16]bool{
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
}

func (s *session) run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	g.Go("MGR stream", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-s.cfgComplete:
		}
		return client.Retry(c, "MGR stream", func(c context.Context) error {
			ver, err := s.managerClient.Version(c, &empty.Empty{})
			if err != nil {
				return err
			}
			verStr := strings.TrimPrefix(ver.Version, "v")
			dlog.Infof(c, "Connected to Manager %s", verStr)
			mgrVer, err := semver.Parse(verStr)
			if err != nil {
				return fmt.Errorf("failed to parse manager version %q: %s", verStr, err)
			}
			if mgrVer.LE(semver.MustParse("2.4.4")) {
				return errcat.User.Newf("unsupported traffic-manager version %s. Minimum supported version is 2.4.5", mgrVer)
			}
			close(s.tmVerOk)
			return err
		})
	})

	g.Go("TUN reader", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-s.tmVerOk:
		}

		dlog.Debug(c, "TUN read loop starting")

		// bufCh is just a small buffer to enable better parallel processing between
		// the actual TUN reader loop and the packet handlers.
		bufCh := make(chan *buffer.Data, 100)
		defer close(bufCh)

		go func() {
			for data := range bufCh {
				s.handlePacket(c, data)
			}
		}()

		for atomic.LoadInt32(&s.closing) < 2 {
			data := buffer.DataPool.Get(buffer.DataPool.MTU)
			for {
				n, err := s.dev.ReadPacket(data)
				if err != nil {
					buffer.DataPool.Put(data)
					if c.Err() != nil || atomic.LoadInt32(&s.closing) == 2 {
						return nil
					}
					return fmt.Errorf("read packet error: %w", err)
				}
				if n > 0 {
					data.SetLength(n)
					bufCh <- data
					break
				}
			}
		}
		return nil
	})
	return g.Wait()
}

func (s *session) handlePacket(c context.Context, data *buffer.Data) {
	defer func() {
		if data != nil {
			buffer.DataPool.Put(data)
		}
	}()

	reply := func(pkt ip.Packet) {
		_, err := s.dev.WritePacket(pkt.Data(), 0)
		if err != nil {
			dlog.Errorf(c, "TUN write failed: %v", err)
		}
	}

	ipHdr, err := ip.ParseHeader(data.Buf())
	if err != nil {
		dlog.Error(c, "Unable to parse packet header")
		return
	}

	if ipHdr.PayloadLen() > buffer.DataPool.MTU-ipHdr.HeaderLen() {
		// Packet is too large for us.
		dlog.Error(c, "Packet exceeds MTU")
		reply(icmp.DestinationUnreachablePacket(ipHdr, icmp.MustFragment))
		return
	}

	if ipHdr.Version() == ipv4.Version {
		v4Hdr := ipHdr.(ip.V4Header)
		if v4Hdr.Flags()&ipv4.MoreFragments != 0 || v4Hdr.FragmentOffset() != 0 {
			dlog.Debug(c, "Packet concat")
			data = v4Hdr.ConcatFragments(data, s.fragmentMap)
			if data == nil {
				return
			}
			v4Hdr = data.Buf()
		}
	} // TODO: similar for ipv6 using segments

	switch ipHdr.L4Protocol() {
	case ipproto.TCP:
		s.tcp(c, tcp.PacketFromData(ipHdr, data))
		data = nil
	case ipproto.UDP:
		dst := ipHdr.Destination()
		if !dst.IsGlobalUnicast() {
			// Just ignore at this point.
			return
		}
		if ip4 := dst.To4(); ip4 != nil && ip4[2] == 0 && ip4[3] == 0 {
			// Write to the a subnet's zero address. Not sure why this is happening but there's no point in
			// passing them on.
			reply(icmp.DestinationUnreachablePacket(ipHdr, icmp.HostUnreachable))
			return
		}
		dg := udp.DatagramFromData(ipHdr, data)
		if blockedUDPPorts[dg.Header().SourcePort()] || blockedUDPPorts[dg.Header().DestinationPort()] {
			reply(icmp.DestinationUnreachablePacket(ipHdr, icmp.PortUnreachable))
			return
		}
		data = nil
		s.udp(c, dg)
	case ipproto.ICMP:
	case ipproto.ICMPV6:
		pkt := icmp.PacketFromData(ipHdr, data)
		dlog.Tracef(c, "<- TUN %s", pkt)
	default:
		// An L4 protocol that we don't handle.
		dlog.Tracef(c, "Unhandled protocol %d", ipHdr.L4Protocol())
		reply(icmp.DestinationUnreachablePacket(ipHdr, icmp.ProtocolUnreachable))
	}
}

type vifWriter struct {
	*vif.Device
}

func (w vifWriter) Write(ctx context.Context, pkt ip.Packet) (err error) {
	dlog.Tracef(ctx, "-> TUN %s", pkt)
	d := pkt.Data()
	l := len(d.Buf())
	o := 0
	for {
		var n int
		if n, err = w.WritePacket(d, o); err != nil {
			return err
		}
		l -= n
		if l == 0 {
			return nil
		}
		o += n
	}
}

func (s *session) tcp(c context.Context, pkt tcp.Packet) {
	ipHdr := pkt.IPHeader()
	tcpHdr := pkt.Header()
	connID := tunnel.NewConnID(ipproto.TCP, ipHdr.Source(), ipHdr.Destination(), tcpHdr.SourcePort(), tcpHdr.DestinationPort())
	dlog.Tracef(c, "<- TUN %s", pkt)
	if !tcpHdr.SYN() {
		// Only a SYN packet can create a new connection. For all other packets, the connection must already exist
		wf := s.handlers.Get(connID)
		if wf == nil {
			pkt.Release()
		} else {
			wf.(tcp.PacketHandler).HandlePacket(c, pkt)
		}
		return
	}

	if tcpHdr.DestinationPort() == s.dnsPort && ipHdr.Destination().Equal(s.dnsIP) {
		// Ignore TCP packets intended for the DNS resolver for now
		// TODO: Add support to DNS over TCP. The github.com/miekg/dns can do that.
		pkt.Release()
		return
	}

	wf, _, err := s.handlers.GetOrCreateTCP(c, connID, func(c context.Context, remove func()) (tunnel.Handler, error) {
		return tcp.NewHandler(s.streamCreator(connID), &s.closing, vifWriter{s.dev}, connID, remove, s.rndSource), nil
	}, pkt)
	if err != nil {
		dlog.Error(c, err)
		pkt.Release()
		return
	}
	wf.(tcp.PacketHandler).HandlePacket(c, pkt)
}

func (s *session) udp(c context.Context, dg udp.Datagram) {
	ipHdr := dg.IPHeader()
	udpHdr := dg.Header()
	connID := tunnel.NewConnID(ipproto.UDP, ipHdr.Source(), ipHdr.Destination(), udpHdr.SourcePort(), udpHdr.DestinationPort())
	uh, _, err := s.handlers.GetOrCreate(c, connID, func(c context.Context, remove func()) (tunnel.Handler, error) {
		w := vifWriter{s.dev}
		if s.dnsLocalAddr != nil && udpHdr.DestinationPort() == s.dnsPort && ipHdr.Destination().Equal(s.dnsIP) {
			return udp.NewDnsInterceptor(w, connID, remove, s.dnsLocalAddr)
		}
		stream, err := s.streamCreator(connID)(c)
		if err != nil {
			return nil, err
		}
		return udp.NewHandler(stream, w, connID, remove), nil
	})
	if err != nil {
		dlog.Error(c, err)
		return
	}
	uh.(udp.DatagramHandler).HandleDatagram(c, dg)
}

func (s *session) streamCreator(id tunnel.ConnID) tcp.StreamCreator {
	return func(c context.Context) (tunnel.Stream, error) {
		dlog.Debugf(c, "Opening tunnel for id %s", id)
		ct, err := s.managerClient.Tunnel(c)
		if err != nil {
			return nil, err
		}
		tc := client.GetConfig(c).Timeouts
		return tunnel.NewClientStream(c, ct, id, s.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}
