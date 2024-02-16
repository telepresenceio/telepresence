package vif

import (
	"context"
	"fmt"
	"net"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func NewStack(ctx context.Context, dev stack.LinkEndpoint, streamCreator tunnel.StreamCreator) (*stack.Stack, error) {
	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
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

const (
	myWindowScale    = 6
	maxReceiveWindow = 1 << (myWindowScale + 14) // 1MiB
)

// maxInFlight specifies the max number of in-flight connection attempts.
const maxInFlight = 512

// keepAliveIdle is used as the very first alive interval. Subsequent intervals
// use keepAliveInterval.
const keepAliveIdle = 60 * time.Second

// keepAliveInterval is the interval between sending keep-alive packets.
const keepAliveInterval = 30 * time.Second

// keepAliveCount is the max number of keep-alive probes that can be sent
// before the connection is killed due to lack of response.
const keepAliveCount = 9

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
	nicID := tcpip.NICID(s.UniqueID())
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

	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "TCPHandler",
		trace.WithNewRoot(),
		trace.WithAttributes(
			attribute.String("tel2.remote-ip", id.RemoteAddress.String()),
			attribute.String("tel2.local-ip", id.LocalAddress.String()),
			attribute.Int("tel2.local-port", int(id.LocalPort)),
			attribute.Int("tel2.remote-port", int(id.RemotePort)),
		))
	defer func() {
		if err != nil {
			msg := fmt.Sprintf("forward TCP %s: %s", idStringer(id), err)
			span.SetStatus(codes.Error, msg)
			dlog.Errorf(ctx, msg)
		}
		span.End()
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

	f := tcp.NewForwarder(s, maxReceiveWindow, maxInFlight, func(fr *tcp.ForwarderRequest) {
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
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "UDPHandler",
		trace.WithNewRoot(),
		trace.WithAttributes(
			attribute.String("tel2.remote-ip", id.RemoteAddress.To4().String()),
			attribute.String("tel2.local-ip", id.LocalAddress.To4().String()),
			attribute.Int("tel2.local-port", int(id.LocalPort)),
			attribute.Int("tel2.remote-port", int(id.RemotePort)),
			attribute.Bool("tel2.port-blocked", false),
		))
	defer span.End()

	if _, ok := blockedUDPPorts[id.LocalPort]; ok {
		span.SetAttributes(attribute.Bool("tel2.port-blocked", true))
		return
	}

	wq := waiter.Queue{}
	ep, err := fr.CreateEndpoint(&wq)
	if err != nil {
		msg := fmt.Sprintf("forward UDP %s: %s", idStringer(id), err)
		span.SetStatus(codes.Error, msg)
		dlog.Errorf(ctx, msg)
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

func newConnID(proto tcpip.TransportProtocolNumber, id stack.TransportEndpointID) tunnel.ConnID {
	return tunnel.NewConnID(int(proto), id.RemoteAddress.AsSlice(), id.LocalAddress.AsSlice(), id.RemotePort, id.LocalPort)
}

func dispatchToStream(ctx context.Context, id tunnel.ConnID, conn net.Conn, streamCreator tunnel.StreamCreator) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := streamCreator(ctx, id)
	if err != nil {
		dlog.Errorf(ctx, "forward %s: %s", id, err)
		cancel()
		return
	}
	ep := tunnel.NewConnEndpoint(stream, conn, cancel, nil, nil)
	ep.Start(ctx)
}
