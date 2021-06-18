package daemon

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/icmp"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/tcp"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/udp"
)

// tunRouter is a router for outbound traffic that is centered around a TUN device. It's similar to a
// TUN-to-SOCKS5 but uses a bidirectional gRPC tunnel instead of SOCKS when communicating with the
// traffic-manager. The addresses of the device are derived from IP addresses sent to it from the user
// daemon (which in turn receives them from the cluster).
//
// Data sent to the device is received as L3 IP-packages and parsed into L4 UDP and TCP before they
// are dispatched over the tunnel. Returned payloads are wrapped as IP-packages before written
// back to the device.
//
// Connection pooling:
//
// For UDP and TCP packages, a ConnID is created which uniquely identifies a combination of protocol,
// source IP, source port, destination IP, and destination port. A handler is then obtained that matches
// that ID (active handlers are cached in a connpool.Pool) and the package is then sent to that handler.
// The handler typically sends the ConnID and the payload of the package over to the traffic-manager
// using the gRPC ClientTunnel. At the receiving en din the traffic-manager, a similar connpool.Pool obtains
// a corresponding handler which manages a net.Conn matching the ConnID in the cluster.
//
// Negotiation:
//
// UDP is of course very simple. It's fire and forget. There's no negotiation whatsoever.
//
// TCP requires a complete workflow engine on the TUN-device side (see tcp.Handler). All TCP negotiation,
// takes place in the client and the same bidirectional tunnel is then used to send both TCP and UDP
// packages to the manager. TCP will send some control packages. One to verify that a connection can
// be established at the manager side, and one when the connection is closed (from either side).
type tunRouter struct {
	// dev is the TUN device that gets configured with the subnets found in the cluster
	dev *tun.Device

	// managerClient provides the gRPC tunnel to the traffic-manager
	managerClient manager.ManagerClient

	// connStream is the bidirectional gRPC tunnel to the traffic-manager
	connStream *connpool.Stream

	// connPool contains handlers that represent active connections. Those handlers
	// are obtained using a connpool.ConnID.
	handlers *connpool.Pool

	// toTunCh  is where handlers post packages intended to be written to the TUN device
	toTunCh chan ip.Packet

	// fragmentMap is when concatenating ipv4 fragments
	fragmentMap map[uint16][]*buffer.Data

	// dnsIP is the IP of the DNS server attached to the TUN device. This is currently only
	// used in conjunction with systemd.resolved. The current MacOS and the overriding solution
	// will dispatch directly to the local DNS service without going through the TUN device but
	// that may change later if we decide to dispatch to the DNS-server in the cluster.
	dnsIP   net.IP
	dnsPort uint16

	// dnsLocalAddr is the address of the local DNS server
	dnsLocalAddr *net.UDPAddr

	// closing is set during shutdown and can have the values:
	//   0 = running
	//   1 = closing
	//   2 = closed
	closing int32

	// session contains the manager session
	session *manager.SessionInfo

	// mgrConfigured will be closed as soon as the connector has sent over the correct port to
	// the traffic manager and the managerClient has been connected.
	mgrConfigured <-chan struct{}

	// rndSource is the source for the random number generator in the TCP handlers
	rndSource rand.Source
}

func newTunRouter(managerConfigured <-chan struct{}) (*tunRouter, error) {
	td, err := tun.OpenTun()
	if err != nil {
		return nil, err
	}
	return &tunRouter{
		dev:           td,
		handlers:      connpool.NewPool(),
		toTunCh:       make(chan ip.Packet, 100),
		mgrConfigured: managerConfigured,
		fragmentMap:   make(map[uint16][]*buffer.Data),
		rndSource:     rand.NewSource(time.Now().UnixNano()),
	}, nil
}

func (t *tunRouter) configureDNS(_ context.Context, dnsIP net.IP, dnsPort uint16, dnsLocalAddr *net.UDPAddr) error {
	t.dnsIP = dnsIP
	t.dnsPort = dnsPort
	t.dnsLocalAddr = dnsLocalAddr
	return nil
}

func (t *tunRouter) setOutboundInfo(ctx context.Context, mi *daemon.OutboundInfo) (err error) {
	if t.managerClient == nil {
		// First check. Establish connection
		tos := &client.GetConfig(ctx).Timeouts
		tc, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
		defer cancel()

		var conn *grpc.ClientConn
		conn, err = grpc.DialContext(tc, fmt.Sprintf("127.0.0.1:%d", mi.ManagerPort),
			grpc.WithInsecure(),
			grpc.WithNoProxy(),
			grpc.WithBlock())
		if err != nil {
			return client.CheckTimeout(tc, err)
		}
		t.session = mi.Session
		t.managerClient = manager.NewManagerClient(conn)

		cidr := iputil.IPNetFromRPC(mi.ServiceSubnet)
		dlog.Infof(ctx, "Adding service subnet %s", cidr)
		if err = t.dev.AddSubnet(ctx, cidr); err != nil {
			return err
		}

		for _, sn := range mi.PodSubnets {
			cidr = iputil.IPNetFromRPC(sn)
			dlog.Infof(ctx, "Adding pod subnet %s", cidr)
			if err = t.dev.AddSubnet(ctx, cidr); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *tunRouter) stop(c context.Context) {
	cc, cancel := context.WithTimeout(c, time.Second)
	defer cancel()
	go func() {
		atomic.StoreInt32(&t.closing, 1)
		t.handlers.CloseAll(cc)
		cancel()
	}()
	<-cc.Done()
	atomic.StoreInt32(&t.closing, 2)
	t.dev.Close()
}

var blockedUDPPorts = map[uint16]bool{
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
}

func (t *tunRouter) run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	// writer
	g.Go("TUN writer", func(c context.Context) error {
		for atomic.LoadInt32(&t.closing) < 2 {
			select {
			case <-c.Done():
				return nil
			case pkt := <-t.toTunCh:
				dlog.Debugf(c, "-> TUN %s", pkt)
				_, err := t.dev.WritePacket(pkt.Data())
				pkt.SoftRelease()
				if err != nil {
					if atomic.LoadInt32(&t.closing) == 2 || c.Err() != nil {
						err = nil
					}
					return err
				}
			}
		}
		return nil
	})

	g.Go("MGR stream", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-t.mgrConfigured:
		}

		tunnel, err := t.managerClient.ClientTunnel(c)
		if err != nil {
			return err
		}
		if err = tunnel.Send(connpool.SessionInfoControl(t.session).TunnelMessage()); err != nil {
			return err
		}
		t.connStream = connpool.NewStream(tunnel)
		dlog.Debug(c, "MGR read loop starting")
		return t.connStream.DialLoop(c, &t.closing, t.handlers)
	})

	g.Go("TUN reader", func(c context.Context) error {
		dlog.Debug(c, "Waiting until manager gRPC is configured")
		select {
		case <-c.Done():
			return nil
		case <-t.mgrConfigured:
		}

		dlog.Debug(c, "TUN read loop starting")
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
					break
				}
			}
			t.handlePacket(c, data)
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

	ipHdr, err := ip.ParseHeader(data.Buf())
	if err != nil {
		dlog.Error(c, "Unable to parse package header")
		return
	}

	if ipHdr.PayloadLen() > buffer.DataPool.MTU-ipHdr.HeaderLen() {
		// Package is too large for us.
		t.toTunCh <- icmp.DestinationUnreachablePacket(ipHdr, icmp.MustFragment)
		return
	}

	if ipHdr.Version() == ipv4.Version {
		v4Hdr := ipHdr.(ip.V4Header)
		if v4Hdr.Flags()&ipv4.MoreFragments != 0 || v4Hdr.FragmentOffset() != 0 {
			data = v4Hdr.ConcatFragments(data, t.fragmentMap)
			if data == nil {
				return
			}
			v4Hdr = data.Buf()
		}
	} // TODO: similar for ipv6 using segments

	switch ipHdr.L4Protocol() {
	case unix.IPPROTO_TCP:
		t.tcp(c, tcp.PacketFromData(ipHdr, data))
		data = nil
	case unix.IPPROTO_UDP:
		dst := ipHdr.Destination()
		if !dst.IsGlobalUnicast() {
			// Just ignore at this point.
			return
		}
		if ip4 := dst.To4(); ip4 != nil && ip4[2] == 0 && ip4[3] == 0 {
			// Write to the a subnet's zero address. Not sure why this is happening but there's no point in
			// passing them on.
			t.toTunCh <- icmp.DestinationUnreachablePacket(ipHdr, icmp.HostUnreachable)
			return
		}
		dg := udp.DatagramFromData(ipHdr, data)
		if blockedUDPPorts[dg.Header().SourcePort()] || blockedUDPPorts[dg.Header().DestinationPort()] {
			t.toTunCh <- icmp.DestinationUnreachablePacket(ipHdr, icmp.PortUnreachable)
			return
		}
		data = nil
		t.udp(c, dg)
	case unix.IPPROTO_ICMP:
	case unix.IPPROTO_ICMPV6:
		pkt := icmp.PacketFromData(ipHdr, data)
		dlog.Debugf(c, "<- TUN %s", pkt)
	default:
		// An L4 protocol that we don't handle.
		t.toTunCh <- icmp.DestinationUnreachablePacket(ipHdr, icmp.ProtocolUnreachable)
	}
}

func (t *tunRouter) tcp(c context.Context, pkt tcp.Packet) {
	ipHdr := pkt.IPHeader()
	tcpHdr := pkt.Header()
	if tcpHdr.DestinationPort() == t.dnsPort && ipHdr.Destination().Equal(t.dnsIP) {
		// Ignore TCP packages intended for the DNS resolver for now
		// TODO: Add support to DNS over TCP. The github.com/miekg/dns can do that.
		return
	}

	connID := connpool.NewConnID(unix.IPPROTO_TCP, ipHdr.Source(), ipHdr.Destination(), tcpHdr.SourcePort(), tcpHdr.DestinationPort())
	wf, _, err := t.handlers.Get(c, connID, func(c context.Context, remove func()) (connpool.Handler, error) {
		return tcp.NewHandler(t.connStream, &t.closing, t.toTunCh, connID, remove, t.rndSource), nil
	})
	if err != nil {
		dlog.Error(c, err)
		return
	}
	wf.(tcp.PacketHandler).HandlePacket(c, pkt)
}

func (t *tunRouter) udp(c context.Context, dg udp.Datagram) {
	ipHdr := dg.IPHeader()
	udpHdr := dg.Header()
	connID := connpool.NewConnID(unix.IPPROTO_UDP, ipHdr.Source(), ipHdr.Destination(), udpHdr.SourcePort(), udpHdr.DestinationPort())
	uh, _, err := t.handlers.Get(c, connID, func(c context.Context, remove func()) (connpool.Handler, error) {
		if udpHdr.DestinationPort() == t.dnsPort && ipHdr.Destination().Equal(t.dnsIP) {
			return udp.NewDnsInterceptor(t.connStream, t.toTunCh, connID, remove, t.dnsLocalAddr)
		}
		return udp.NewHandler(t.connStream, t.toTunCh, connID, remove), nil
	})
	if err != nil {
		dlog.Error(c, err)
		return
	}
	uh.(udp.DatagramHandler).NewDatagram(c, dg)
}
