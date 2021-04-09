package daemon

import (
	"context"
	"fmt"
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
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/icmp"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/tcp"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/udp"
)

// IPKey is an immutable cast of a net.IP suitable to be used as a map key. It must be created using IPKey(ip)
type IPKey string

func (k IPKey) IP() net.IP {
	return net.IP(k)
}

// String returns the human readable string form of the IP (as opposed to the binary junk displayed when using it directly).
func (k IPKey) String() string {
	return net.IP(k).String()
}

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
// using the gRPC ConnTunnel. At the receiving en din the traffic-manager, a similar connpool.Pool obtains
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

	// mgrConfigured will be closed as soon as the connector has sent over the correct port to
	// the traffic manager and the managerClient has been connected.
	mgrConfigured <-chan struct{}

	// ips is the current set of IP addresses mapped by this router.
	ips map[IPKey]struct{}

	// subnets is the current set of subnets mapped by this router. It is derived from ips.
	subnets map[string]*net.IPNet
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
		ips:           make(map[IPKey]struct{}),
		subnets:       make(map[string]*net.IPNet),
	}, nil
}

// snapshot returns a copy of the current IP table.
func (t *tunRouter) snapshot() map[IPKey]struct{} {
	ips := make(map[IPKey]struct{}, len(t.ips))
	for k, v := range t.ips {
		ips[k] = v
	}
	return ips
}

// clear the given ip. Returns true if the ip was cleared and false if not found.
func (t *tunRouter) clear(_ context.Context, ip IPKey) (found bool) {
	if _, found = t.ips[ip]; found {
		delete(t.ips, ip)
	}
	return found
}

// add the given ip. Returns true if the io was added and false if it was already present.
func (t *tunRouter) add(_ context.Context, ip IPKey) (found bool) {
	if _, found = t.ips[ip]; !found {
		t.ips[ip] = struct{}{}
	}
	return !found
}

// flush any pending changes that needs to be committed
func (t *tunRouter) flush(c context.Context, dnsIP net.IP) error {
	addedNets := make(map[string]*net.IPNet)
	ips := make([]net.IP, len(t.ips))
	i := 0
	for ip := range t.ips {
		ips[i] = net.IP(ip)
		i++
	}

	droppedNets := make(map[string]*net.IPNet)
	for _, sn := range subnet.AnalyzeIPs(ips) {
		// TODO: Figure out how networks cover each other, merge and remove as needed.
		// For now, we just have one 16-bit mask for the whole subnet
		alreadyCovered := false
		for _, esn := range t.subnets {
			if subnet.Covers(esn, sn) {
				alreadyCovered = true
				break
			}
		}
		if !alreadyCovered {
			for k, esn := range t.subnets {
				if subnet.Covers(sn, esn) {
					droppedNets[k] = esn
					break
				}
			}
			addedNets[sn.String()] = sn
		}
	}

	for k, dropped := range droppedNets {
		if err := t.dev.RemoveSubnet(c, dropped); err != nil {
			return err
		}
		delete(t.subnets, k)
	}

	if len(addedNets) == 0 {
		return nil
	}

	subnets := make([]*net.IPNet, len(addedNets))
	i = 0
	for k, sn := range addedNets {
		t.subnets[k] = sn
		if i > 0 && dnsIP != nil && sn.Contains(dnsIP) {
			// Ensure that the subnet for the DNS is placed first
			subnets[0], sn = sn, subnets[0]
		}
		subnets[i] = sn
		i++
	}
	for _, sn := range subnets {
		dlog.Debugf(c, "Adding subnet %s", sn)
		if err := t.dev.AddSubnet(c, sn); err != nil {
			return err
		}
	}
	return nil
}

func (t *tunRouter) configureDNS(_ context.Context, dnsIP net.IP, dnsPort uint16, dnsLocalAddr *net.UDPAddr) error {
	t.dnsIP = dnsIP
	t.dnsPort = dnsPort
	t.dnsLocalAddr = dnsLocalAddr
	return nil
}

func (t *tunRouter) setManagerInfo(ctx context.Context, mi *daemon.ManagerInfo) (err error) {
	if t.managerClient == nil {
		// First check. Establish connection
		tos := &client.GetConfig(ctx).Timeouts
		tc, cancel := context.WithTimeout(ctx, tos.TrafficManagerAPI)
		defer cancel()

		var conn *grpc.ClientConn
		conn, err = grpc.DialContext(tc, fmt.Sprintf("127.0.0.1:%d", mi.GrpcPort),
			grpc.WithInsecure(),
			grpc.WithNoProxy(),
			grpc.WithBlock())
		if err != nil {
			return client.CheckTimeout(tc, &tos.TrafficManagerAPI, err)
		}
		t.managerClient = manager.NewManagerClient(conn)
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
				_, err := t.dev.Write(pkt.Data())
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

		// TODO: ConnTunnel should probably provide a sessionID
		tunnel, err := t.managerClient.ConnTunnel(c)
		if err != nil {
			return err
		}
		t.connStream = connpool.NewStream(tunnel, t.handlers)
		dlog.Debug(c, "MGR read loop starting")
		return t.connStream.ReadLoop(c, &t.closing)
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
				n, err := t.dev.Read(data)
				if err != nil {
					buffer.DataPool.Put(data)
					if c.Err() != nil || atomic.LoadInt32(&t.closing) == 2 {
						return nil
					}
					return fmt.Errorf("read packet error: %v", err)
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
		t.toTunCh <- icmp.DestinationUnreachablePacket(uint16(buffer.DataPool.MTU), ipHdr, icmp.MustFragment)
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
		if dst.IsLinkLocalUnicast() || dst.IsLinkLocalMulticast() {
			// Just ignore at this point.
			return
		}
		if ip4 := dst.To4(); ip4 != nil && ip4[2] == 0 && ip4[3] == 0 {
			// Write to the a subnet's zero address. Not sure why this is happening but there's no point in
			// passing them on.
			t.toTunCh <- icmp.DestinationUnreachablePacket(uint16(buffer.DataPool.MTU), ipHdr, icmp.HostUnreachable)
			return
		}
		dg := udp.DatagramFromData(ipHdr, data)
		if blockedUDPPorts[dg.Header().SourcePort()] || blockedUDPPorts[dg.Header().DestinationPort()] {
			t.toTunCh <- icmp.DestinationUnreachablePacket(uint16(buffer.DataPool.MTU), ipHdr, icmp.PortUnreachable)
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
		t.toTunCh <- icmp.DestinationUnreachablePacket(uint16(buffer.DataPool.MTU), ipHdr, icmp.ProtocolUnreachable)
	}
}

func (t *tunRouter) tcp(c context.Context, pkt tcp.Packet) {
	ipHdr := pkt.IPHeader()
	tcpHdr := pkt.Header()
	connID := connpool.NewConnID(unix.IPPROTO_TCP, ipHdr.Source(), ipHdr.Destination(), tcpHdr.SourcePort(), tcpHdr.DestinationPort())
	wf, err := t.handlers.Get(c, connID, func(c context.Context, remove func()) (connpool.Handler, error) {
		return tcp.NewHandler(t.connStream, &t.closing, t.toTunCh, connID, remove), nil
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
	uh, err := t.handlers.Get(c, connID, func(c context.Context, remove func()) (connpool.Handler, error) {
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
