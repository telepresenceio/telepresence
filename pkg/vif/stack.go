package vif

import (
	"context"
	"fmt"
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/log"
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
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type dlogEmitter struct {
	context.Context
}

func (l dlogEmitter) Emit(_ int, level log.Level, _ time.Time, format string, v ...interface{}) {
	switch level {
	case log.Debug:
		dlog.Debugf(l, format, v...)
	case log.Info:
		dlog.Infof(l, format, v...)
	case log.Warning:
		dlog.Warnf(l, format, v...)
	}
}

func InitLogger(ctx context.Context) {
	log.SetTarget(&dlogEmitter{Context: ctx})
	var gl log.Level
	switch dlog.MaxLogLevel(ctx) {
	case dlog.LogLevelInfo:
		gl = log.Info
	case dlog.LogLevelDebug, dlog.LogLevelTrace:
		gl = log.Debug
	default:
		gl = log.Warning
	}
	log.SetLevel(gl)
}

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

const myWindowScale = 6
const maxReceiveWindow = 1 << (myWindowScale + 14) // 1MiB

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
	return fmt.Sprintf("%s:%d -> %s:%d", i.RemoteAddress, i.RemotePort, i.LocalAddress, i.LocalPort)
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

func setTCPHandler(ctx context.Context, s *stack.Stack, streamCreator tunnel.StreamCreator) {
	f := tcp.NewForwarder(s, maxReceiveWindow, maxInFlight, func(fr *tcp.ForwarderRequest) {
		var ep tcpip.Endpoint
		var err tcpip.Error
		id := fr.ID()
		defer func() {
			if err != nil {
				dlog.Errorf(ctx, "forward TCP %s: %s", idStringer(id), err)
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

		var rs tcpip.TCPReceiveBufferSizeRangeOption
		if err = s.TransportProtocolOption(header.TCPProtocolNumber, &rs); err != nil {
			return
		}
		so.SetReceiveBufferSize(int64(rs.Default), false)

		var ss tcpip.TCPSendBufferSizeRangeOption
		if err = s.TransportProtocolOption(header.TCPProtocolNumber, &ss); err != nil {
			return
		}
		so.SetSendBufferSize(int64(ss.Default), false)

		if err = s.SetTransportProtocolOption(tcp.ProtocolNumber,
			&tcpip.TCPSendBufferSizeRangeOption{
				Min:     tcp.MinBufferSize,
				Default: tcp.DefaultSendBufferSize,
				Max:     tcp.MaxBufferSize,
			}); err != nil {
			return
		}

		if err = s.SetTransportProtocolOption(tcp.ProtocolNumber,
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

		dispatchToStream(ctx, newConnID(header.TCPProtocolNumber, id), gonet.NewTCPConn(&wq, ep), streamCreator)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, f.HandlePacket)
}

var blockedUDPPorts = map[uint16]bool{
	137: true, // NETBIOS Name Service
	138: true, // NETBIOS Datagram Service
	139: true, // NETBIOS
}

func setUDPHandler(ctx context.Context, s *stack.Stack, streamCreator tunnel.StreamCreator) {
	f := udp.NewForwarder(s, func(fr *udp.ForwarderRequest) {
		if _, ok := blockedUDPPorts[fr.ID().LocalPort]; ok {
			return
		}

		wq := waiter.Queue{}
		ep, err := fr.CreateEndpoint(&wq)
		if err != nil {
			dlog.Errorf(ctx, "forward UDP %s: %s", idStringer(fr.ID()), err)
			return
		}
		dispatchToStream(ctx, newConnID(udp.ProtocolNumber, fr.ID()), gonet.NewUDPConn(s, &wq, ep), streamCreator)
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, f.HandlePacket)
}

func newConnID(proto tcpip.TransportProtocolNumber, id stack.TransportEndpointID) tunnel.ConnID {
	return tunnel.NewConnID(int(proto), ([]byte)(id.RemoteAddress), ([]byte)(id.LocalAddress), id.RemotePort, id.LocalPort)
}

func dispatchToStream(ctx context.Context, id tunnel.ConnID, conn net.Conn, streamCreator tunnel.StreamCreator) {
	stream, err := streamCreator(ctx, id)
	if err != nil {
		if err != nil {
			dlog.Errorf(ctx, "forward %s: %s", id, err)
		}
		return
	}
	ep := tunnel.NewConnEndpoint(stream, conn)
	ep.Start(ctx)
}
