package vif

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func NewStack(ctx context.Context, dev stack.LinkEndpoint, streamCreator tunnel.StreamCreator) (*stack.Stack, error) {
	pfs := []stack.NetworkProtocolFactory{
		ipv4.NewProtocol,
		ipv6.NewProtocol,
	}
	if client.GetConfig(ctx).Routing().UseTAP {
		pfs = append(pfs, arp.NewProtocol)
	}
	s := stack.New(stack.Options{
		NetworkProtocols: pfs,
		TransportProtocols: []stack.TransportProtocolFactory{
			icmp.NewProtocol4,
			icmp.NewProtocol6,
			tcp.NewProtocol,
			udp.NewProtocol,
		},
		HandleLocal: false,
	})
	if err := setDefaultOptions(s); err != nil {
		return nil, err
	}
	if err := setNIC(ctx, s, dev); err != nil {
		return nil, err
	}
	setTCPHandler(ctx, s, streamCreator)
	setUDPHandler(ctx, s, streamCreator)
	return s, nil
}

// maxInFlight specifies the max number of in-flight connection attempts.
const maxInFlight = 1024

// keepAliveIdle is used as the very first keep-alive interval. Subsequent intervals
// use keepAliveInterval.
const keepAliveIdle = 18 * time.Second

// keepAliveInterval is the interval between sending keep-alive packets. We keep this fairly short
// because we don't worry too much about draining batteries on cellphones.
const keepAliveInterval = 9 * time.Second

// keepAliveCount is the max number of keep-alive probes that can be sent
// before the connection is killed due to lack of response.
const keepAliveCount = 10

type idStringer stack.TransportEndpointID

func (i idStringer) String() string {
	return fmt.Sprintf("%s -> %s",
		iputil.JoinIpPort(i.RemoteAddress.AsSlice(), i.RemotePort),
		iputil.JoinIpPort(i.LocalAddress.AsSlice(), i.LocalPort))
}

func setDefaultOptions(s *stack.Stack) error {
	// Forwarding
	if err := s.SetForwardingDefaultAndAllNICs(ipv4.ProtocolNumber, true); err != nil {
		return fmt.Errorf("SetForwardingDefaultAndAllNICs(ipv4, %t): %s", true, err)
	}
	if err := s.SetForwardingDefaultAndAllNICs(ipv6.ProtocolNumber, true); err != nil {
		return fmt.Errorf("SetForwardingDefaultAndAllNICs(ipv6, %t): %s", true, err)
	}
	ttl := tcpip.DefaultTTLOption(64)
	if err := s.SetNetworkProtocolOption(ipv4.ProtocolNumber, &ttl); err != nil {
		return fmt.Errorf("SetDefaultTTL(ipv4, %d): %s", ttl, err)
	}
	if err := s.SetNetworkProtocolOption(ipv6.ProtocolNumber, &ttl); err != nil {
		return fmt.Errorf("SetDefaultTTL(ipv6, %d): %s", ttl, err)
	}
	return nil
}

func setNIC(ctx context.Context, s *stack.Stack, ep stack.LinkEndpoint) error {
	nicID := s.NextNICID()
	if err := s.CreateNICWithOptions(nicID, ep, stack.NICOptions{Name: "tel", Context: ctx}); err != nil {
		return fmt.Errorf("create NIC failed: %s", err)
	}
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return fmt.Errorf("SetPromiscuousMode(%d, %t): %s", nicID, true, err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return fmt.Errorf("SetSpoofing(%d, %t): %s", nicID, true, err)
	}
	s.SetRouteTable([]tcpip.Route{
		{
			Destination: header.IPv4EmptySubnet,
			NIC:         nicID,
		},
		{
			Destination: header.IPv6EmptySubnet,
			NIC:         nicID,
		},
	})
	return nil
}

func forwardTCP(ctx context.Context, streamCreator tunnel.StreamCreator, fr *tcp.ForwarderRequest) {
	var ep tcpip.Endpoint
	var err tcpip.Error
	id := fr.ID()
	defer func() {
		if err != nil {
			msg := fmt.Sprintf("forward TCP %s: %s", idStringer(id), err)
			dlog.Error(ctx, msg)
		}
	}()

	wq := waiter.Queue{}
	if ep, err = fr.CreateEndpoint(&wq); err != nil {
		fr.Complete(true)
		return
	}
	defer fr.Complete(false)

	so := ep.SocketOptions()
	so.SetKeepAlive(true)

	idle := tcpip.KeepaliveIdleOption(keepAliveIdle)
	if err = ep.SetSockOpt(&idle); err != nil {
		return
	}

	ivl := tcpip.KeepaliveIntervalOption(keepAliveInterval)
	if err = ep.SetSockOpt(&ivl); err != nil {
		return
	}

	if err = ep.SetSockOptInt(tcpip.KeepaliveCountOption, keepAliveCount); err != nil {
		return
	}
	dispatchToStream(ctx, newConnID(header.TCPProtocolNumber, id), gonet.NewTCPConn(&wq, ep), streamCreator)
}

func setTCPHandler(ctx context.Context, s *stack.Stack, streamCreator tunnel.StreamCreator) {
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber,
		&tcpip.TCPSendBufferSizeRangeOption{
			Min:     tcp.MinBufferSize,
			Default: tcp.DefaultSendBufferSize,
			Max:     tcp.MaxBufferSize,
		}); err != nil {
		return
	}

	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber,
		&tcpip.TCPReceiveBufferSizeRangeOption{
			Min:     tcp.MinBufferSize,
			Default: tcp.DefaultSendBufferSize,
			Max:     tcp.MaxBufferSize,
		}); err != nil {
		return
	}

	sa := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sa)

	// Enable Receive Buffer Auto-Tuning, see:
	// https://github.com/google/gvisor/issues/1666
	mo := tcpip.TCPModerateReceiveBufferOption(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &mo)

	f := tcp.NewForwarder(s, 0, maxInFlight, func(fr *tcp.ForwarderRequest) {
		forwardTCP(ctx, streamCreator, fr)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, f.HandlePacket)
}

var blockedUDPPorts = map[uint16]bool{ //nolint:gochecknoglobals // constant
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
}

func forwardUDP(ctx context.Context, streamCreator tunnel.StreamCreator, fr *udp.ForwarderRequest) {
	id := fr.ID()
	if _, ok := blockedUDPPorts[id.LocalPort]; ok {
		return
	}

	wq := waiter.Queue{}
	ep, err := fr.CreateEndpoint(&wq)
	if err != nil {
		msg := fmt.Sprintf("forward UDP %s: %s", idStringer(id), err)
		dlog.Error(ctx, msg)
		return
	}
	dispatchToStream(ctx, newConnID(udp.ProtocolNumber, id), gonet.NewUDPConn(&wq, ep), streamCreator)
}

func setUDPHandler(ctx context.Context, s *stack.Stack, streamCreator tunnel.StreamCreator) {
	f := udp.NewForwarder(s, func(fr *udp.ForwarderRequest) {
		forwardUDP(ctx, streamCreator, fr)
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, f.HandlePacket)
}

func tcpAddrToAddr(addr tcpip.Address) netip.Addr {
	if addr.BitLen() == 32 {
		return netip.AddrFrom4(addr.As4())
	}
	return netip.AddrFrom16(addr.As16())
}

func newConnID(proto tcpip.TransportProtocolNumber, id stack.TransportEndpointID) tunnel.ConnID {
	return tunnel.NewConnID(int(proto), netip.AddrPortFrom(tcpAddrToAddr(id.RemoteAddress), id.RemotePort), netip.AddrPortFrom(tcpAddrToAddr(id.LocalAddress), id.LocalPort))
}

func dispatchToStream(ctx context.Context, id tunnel.ConnID, conn net.Conn, streamCreator tunnel.StreamCreator) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := streamCreator(ctx, id)
	if err != nil {
		dlog.Errorf(ctx, "forward %s: %v", id, err)
		cancel()
		return
	}
	ep := tunnel.NewConnEndpoint(stream, conn, cancel, nil, nil)
	ep.Start(ctx)
}
