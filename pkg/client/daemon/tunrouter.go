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
	"sync/atomic"
	"time"

	"github.com/blang/semver"
	"golang.org/x/net/ipv4"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
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

// tunRouter is a router for session traffic that is centered around a TUN device. It's similar to a
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
type tunRouter struct {
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

func newTunRouter(ctx context.Context) (*tunRouter, error) {
	td, err := vif.OpenTun(ctx)
	if err != nil {
		return nil, err
	}
	return &tunRouter{
		dev:         td,
		handlers:    tunnel.NewPool(),
		cfgComplete: make(chan struct{}),
		tmVerOk:     make(chan struct{}),
		fragmentMap: make(map[uint16][]*buffer.Data),
		rndSource:   rand.NewSource(time.Now().UnixNano()),
	}, nil
}

func (t *tunRouter) configured() <-chan struct{} {
	return t.tmVerOk
}

func (t *tunRouter) configureDNS(_ context.Context, dnsLocalAddr *net.UDPAddr) {
	t.dnsPort = 53
	t.dnsLocalAddr = dnsLocalAddr
}

func (t *tunRouter) reconcileStaticRoutes(ctx context.Context) error {
	desired := []routing.Route{}

	// We're not going to add static routes unless they're actually needed
	// (i.e. unless the existing CIDRs overlap with the never-proxy subnets)
	for _, r := range t.neverProxySubnets {
		for _, s := range t.curSubnets {
			if s.Contains(r.RoutedNet.IP) || r.Routes(s.IP) {
				desired = append(desired, r)
				break
			}
		}
	}

adding:
	for _, r := range desired {
		for _, c := range t.curStaticRoutes {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue adding
			}
		}
		if err := t.dev.AddStaticRoute(ctx, r); err != nil {
			dlog.Errorf(ctx, "failed to add static route %s: %v", r, err)
		}
	}

removing:
	for _, c := range t.curStaticRoutes {
		for _, r := range desired {
			if subnet.Equal(r.RoutedNet, c.RoutedNet) {
				continue removing
			}
		}
		if err := t.dev.RemoveStaticRoute(ctx, c); err != nil {
			dlog.Errorf(ctx, "failed to remove static route %s: %v", c, err)
		}
	}
	t.curStaticRoutes = desired

	return nil
}

func (t *tunRouter) refreshSubnets(ctx context.Context) error {
	// Create a unique slice of all desired subnets.
	desired := make([]*net.IPNet, len(t.clusterSubnets)+len(t.alsoProxySubnets))
	copy(desired, t.clusterSubnets)
	copy(desired[len(t.clusterSubnets):], t.alsoProxySubnets)
	desired = subnet.Unique(desired)

	// Remove all no longer desired subnets from the t.curSubnets
	var removed []*net.IPNet
	t.curSubnets, removed = subnet.Partition(t.curSubnets, func(_ int, sn *net.IPNet) bool {
		for _, d := range desired {
			if subnet.Equal(sn, d) {
				return true
			}
		}
		return false
	})

	// Remove already routed subnets from the desiredSubnets
	added, _ := subnet.Partition(desired, func(_ int, sn *net.IPNet) bool {
		for _, d := range t.curSubnets {
			if subnet.Equal(sn, d) {
				return false
			}
		}
		return true
	})

	// Add desiredSubnets to the currently routed subnets
	t.curSubnets = append(t.curSubnets, added...)

	for _, sn := range removed {
		if err := t.dev.RemoveSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to remove subnet %s: %v", sn, err)
		}
	}

	for _, sn := range added {
		if err := t.dev.AddSubnet(ctx, sn); err != nil {
			dlog.Errorf(ctx, "failed to add subnet %s: %v", sn, err)
		}
	}

	return t.reconcileStaticRoutes(ctx)
}

func (t *tunRouter) setOutboundInfo(ctx context.Context, mi *daemon.OutboundInfo) (err error) {
	if t.managerClient == nil {
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
		t.session = mi.Session
		t.managerClient = manager.NewManagerClient(conn)

		if len(mi.AlsoProxySubnets) > 0 {
			t.alsoProxySubnets = make([]*net.IPNet, len(mi.AlsoProxySubnets))
			for i, ap := range mi.AlsoProxySubnets {
				apSn := iputil.IPNetFromRPC(ap)
				dlog.Infof(ctx, "Adding also-proxy subnet %s", apSn)
				t.alsoProxySubnets[i] = apSn
			}
		}
		if len(mi.NeverProxySubnets) > 0 {
			t.neverProxySubnets = []routing.Route{}
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
				t.neverProxySubnets = append(t.neverProxySubnets, route)
			}
		}
		t.dnsIP = mi.Dns.RemoteIp

		dgroup.ParentGroup(ctx).Go("watch-cluster-info", func(ctx context.Context) error {
			t.watchClusterInfo(ctx)
			return nil
		})
	}
	return nil
}

func (t *tunRouter) watchClusterInfo(ctx context.Context) {
	cfgComplete := t.cfgComplete
	backoff := 100 * time.Millisecond

	for ctx.Err() == nil {
		infoStream, err := t.managerClient.WatchClusterInfo(ctx, t.session)
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

			t.clusterSubnets = subnets
			if err := t.refreshSubnets(ctx); err != nil {
				dlog.Error(ctx, err)
			}

			if cfgComplete != nil {
				// Only set clusterDNS when it hasn't been explicitly set with the --dns option
				if t.dnsIP == nil {
					dlog.Infof(ctx, "Setting cluster DNS to %s", net.IP(mgrInfo.KubeDnsIp))
					t.dnsIP = mgrInfo.KubeDnsIp
				}
				dlog.Infof(ctx, "Setting cluster domain to %q", mgrInfo.ClusterDomain)
				t.clusterDomain = mgrInfo.ClusterDomain
				if t.clusterDomain == "" {
					// Traffic manager predates 2.4.3 and doesn't report a cluster domain. Only thing
					// left to do then is to assume it's the standard one.
					t.clusterDomain = "cluster.local."
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

func (t *tunRouter) stop(c context.Context) {
	if atomic.CompareAndSwapInt32(&t.closing, 0, 1) {
		cc, cancel := context.WithTimeout(c, time.Second)
		defer cancel()
		go func() {
			atomic.StoreInt32(&t.closing, 1)
			t.handlers.CloseAll(cc)
			cancel()
		}()
		<-cc.Done()
	}
	if atomic.CompareAndSwapInt32(&t.closing, 1, 2) {
		for _, np := range t.curStaticRoutes {
			err := t.dev.RemoveStaticRoute(c, np)
			if err != nil {
				dlog.Warnf(c, "error removing route %s: %v", np, err)
			}
		}
		t.dev.Close()
	}
}

var blockedUDPPorts = map[uint16]bool{
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
}

func (t *tunRouter) run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	g.Go("MGR stream", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-t.cfgComplete:
		}
		return client.Retry(c, "MGR stream", func(c context.Context) error {
			ver, err := t.managerClient.Version(c, &empty.Empty{})
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
			close(t.tmVerOk)
			return err
		})
	})

	g.Go("TUN reader", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-t.tmVerOk:
		}

		dlog.Debug(c, "TUN read loop starting")

		// bufCh is just a small buffer to enable better parallel processing between
		// the actual TUN reader loop and the packet handlers.
		bufCh := make(chan *buffer.Data, 100)
		defer close(bufCh)

		go func() {
			for data := range bufCh {
				t.handlePacket(c, data)
			}
		}()

		for atomic.LoadInt32(&t.closing) < 2 {
			data := buffer.DataPool.Get(buffer.DataPool.MTU)
			for {
				n, err := t.dev.ReadPacket(data)
				if err != nil {
					buffer.DataPool.Put(data)
					if c.Err() != nil || atomic.LoadInt32(&t.closing) == 2 {
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

func (t *tunRouter) handlePacket(c context.Context, data *buffer.Data) {
	defer func() {
		if data != nil {
			buffer.DataPool.Put(data)
		}
	}()

	reply := func(pkt ip.Packet) {
		_, err := t.dev.WritePacket(pkt.Data(), 0)
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
			data = v4Hdr.ConcatFragments(data, t.fragmentMap)
			if data == nil {
				return
			}
			v4Hdr = data.Buf()
		}
	} // TODO: similar for ipv6 using segments

	switch ipHdr.L4Protocol() {
	case ipproto.TCP:
		t.tcp(c, tcp.PacketFromData(ipHdr, data))
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
		t.udp(c, dg)
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

func (t *tunRouter) tcp(c context.Context, pkt tcp.Packet) {
	ipHdr := pkt.IPHeader()
	tcpHdr := pkt.Header()
	connID := tunnel.NewConnID(ipproto.TCP, ipHdr.Source(), ipHdr.Destination(), tcpHdr.SourcePort(), tcpHdr.DestinationPort())
	dlog.Tracef(c, "<- TUN %s", pkt)
	if !tcpHdr.SYN() {
		// Only a SYN packet can create a new connection. For all other packets, the connection must already exist
		wf := t.handlers.Get(connID)
		if wf == nil {
			pkt.Release()
		} else {
			wf.(tcp.PacketHandler).HandlePacket(c, pkt)
		}
		return
	}

	if tcpHdr.DestinationPort() == t.dnsPort && ipHdr.Destination().Equal(t.dnsIP) {
		// Ignore TCP packets intended for the DNS resolver for now
		// TODO: Add support to DNS over TCP. The github.com/miekg/dns can do that.
		pkt.Release()
		return
	}

	wf, _, err := t.handlers.GetOrCreateTCP(c, connID, func(c context.Context, remove func()) (tunnel.Handler, error) {
		return tcp.NewHandler(t.streamCreator(connID), &t.closing, vifWriter{t.dev}, connID, remove, t.rndSource), nil
	}, pkt)
	if err != nil {
		dlog.Error(c, err)
		pkt.Release()
		return
	}
	wf.(tcp.PacketHandler).HandlePacket(c, pkt)
}

func (t *tunRouter) udp(c context.Context, dg udp.Datagram) {
	ipHdr := dg.IPHeader()
	udpHdr := dg.Header()
	connID := tunnel.NewConnID(ipproto.UDP, ipHdr.Source(), ipHdr.Destination(), udpHdr.SourcePort(), udpHdr.DestinationPort())
	uh, _, err := t.handlers.GetOrCreate(c, connID, func(c context.Context, remove func()) (tunnel.Handler, error) {
		w := vifWriter{t.dev}
		if t.dnsLocalAddr != nil && udpHdr.DestinationPort() == t.dnsPort && ipHdr.Destination().Equal(t.dnsIP) {
			return udp.NewDnsInterceptor(w, connID, remove, t.dnsLocalAddr)
		}
		stream, err := t.streamCreator(connID)(c)
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

func (t *tunRouter) streamCreator(id tunnel.ConnID) tcp.StreamCreator {
	return func(c context.Context) (tunnel.Stream, error) {
		dlog.Debugf(c, "Opening tunnel for id %s", id)
		ct, err := t.managerClient.Tunnel(c)
		if err != nil {
			return nil, err
		}
		tc := client.GetConfig(c).Timeouts
		return tunnel.NewClientStream(c, ct, id, t.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}
