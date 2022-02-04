package rootd

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"golang.org/x/net/ipv4"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/icmp"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/tcp"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/udp"
)

func (s *session) routerWorker(c context.Context) error {
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
}

var blockedUDPPorts = map[uint16]bool{
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
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

func (s *session) isForDNS(ip net.IP, port uint16) bool {
	return s.remoteDnsIP != nil && port == 53 && s.remoteDnsIP.Equal(ip)
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

	if s.isForDNS(ipHdr.Destination(), tcpHdr.DestinationPort()) {
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
	// if wf is nil, the packet should simply be ignored
	if wf != nil {
		wf.(tcp.PacketHandler).HandlePacket(c, pkt)
	}
}

func (s *session) udp(c context.Context, dg udp.Datagram) {
	ipHdr := dg.IPHeader()
	udpHdr := dg.Header()
	connID := tunnel.NewConnID(ipproto.UDP, ipHdr.Source(), ipHdr.Destination(), udpHdr.SourcePort(), udpHdr.DestinationPort())
	uh, _, err := s.handlers.GetOrCreate(c, connID, func(c context.Context, remove func()) (tunnel.Handler, error) {
		w := vifWriter{s.dev}
		if s.isForDNS(ipHdr.Destination(), udpHdr.DestinationPort()) {
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
