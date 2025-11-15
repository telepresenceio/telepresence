package vif

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type TunnelingDevice struct {
	stack  *stack.Stack
	nicID  tcpip.NICID
	Device Device
	Router *Router
	table  routing.Table
}

func NewTunnelingDevice(ctx context.Context, tunnelStreamCreator tunnel.StreamCreator) (vif *TunnelingDevice, err error) {
	var (
		routingTable routing.Table
		dev          Device
		ep           stack.LinkEndpoint
		netStack     *stack.Stack
	)
	defer func() {
		if err != nil {
			if netStack != nil {
				netStack.Close()
			}
			if ep != nil {
				ep.Close()
			}
			if dev != nil {
				dev.Close()
			}
			if routingTable != nil {
				routingTable.Close(ctx)
			}
		}
	}()
	routingTable, err = routing.OpenTable(ctx)
	if err != nil {
		return nil, err
	}
	dev, err = OpenTun(ctx)
	if err != nil {
		return nil, err
	}
	ep, err = dev.NewLinkEndpoint()
	if err != nil {
		return nil, err
	}
	netStack, nicID, err := NewStack(ctx, ep, tunnelStreamCreator)
	if err != nil {
		return nil, err
	}
	router := NewRouter(dev, routingTable)
	return &TunnelingDevice{
		stack:  netStack,
		nicID:  nicID,
		Device: dev,
		Router: router,
		table:  routingTable,
	}, nil
}

func (vif *TunnelingDevice) Close(ctx context.Context) error {
	vif.stack.Close()
	vif.Router.Close(ctx)
	vif.Device.Close()
	return vif.table.Close(ctx)
}

func (vif *TunnelingDevice) AddStaticNeighbor(addr netip.Addr, linkAddr net.HardwareAddr) error {
	var proto tcpip.NetworkProtocolNumber
	var tAddr tcpip.Address
	if addr.Is4() {
		proto = ipv4.ProtocolNumber
		tAddr = tcpip.AddrFrom4(addr.As4())
	} else {
		proto = ipv6.ProtocolNumber
		tAddr = tcpip.AddrFrom16(addr.As16())
	}
	tErr := vif.stack.AddStaticNeighbor(vif.nicID, proto, tAddr, tcpip.LinkAddress(linkAddr))
	if tErr != nil {
		return errors.New(tErr.String())
	}
	return nil
}

func (vif *TunnelingDevice) RemoveNeighbor(addr netip.Addr) error {
	var proto tcpip.NetworkProtocolNumber
	var tAddr tcpip.Address
	if addr.Is4() {
		proto = ipv4.ProtocolNumber
		tAddr = tcpip.AddrFrom4(addr.As4())
	} else {
		proto = ipv6.ProtocolNumber
		tAddr = tcpip.AddrFrom16(addr.As16())
	}
	tErr := vif.stack.RemoveNeighbor(vif.nicID, proto, tAddr)
	if tErr != nil {
		return errors.New(tErr.String())
	}
	return nil
}

func (vif *TunnelingDevice) DialTCP(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	p, a := vif.toFullAddr(addr)
	return gonet.DialContextTCP(ctx, vif.stack, a, p)
}

func (vif *TunnelingDevice) DialUDP(_ context.Context, addr, returnAddr netip.AddrPort) (net.Conn, error) {
	var fa, rfa *tcpip.FullAddress
	var p tcpip.NetworkProtocolNumber
	if addr.IsValid() {
		var a tcpip.FullAddress
		p, a = vif.toFullAddr(addr)
		fa = &a
	}
	if returnAddr.IsValid() {
		var a tcpip.FullAddress
		_, a = vif.toFullAddr(addr)
		fa = &a
	}
	return gonet.DialUDP(vif.stack, fa, rfa, p)
}

func (vif *TunnelingDevice) Run(ctx context.Context) (err error) {
	vif.stack.Wait()
	dlog.Debug(ctx, "VIF ended")
	return nil
}

func (vif *TunnelingDevice) toFullAddr(ap netip.AddrPort) (tcpip.NetworkProtocolNumber, tcpip.FullAddress) {
	fa := tcpip.FullAddress{
		NIC:  vif.nicID,
		Port: ap.Port(),
	}
	a := ap.Addr()
	var p tcpip.NetworkProtocolNumber
	if a.Is4() {
		p = ipv4.ProtocolNumber
		fa.Addr = tcpip.AddrFrom4(a.As4())
	} else {
		p = ipv6.ProtocolNumber
		fa.Addr = tcpip.AddrFrom16(a.As16())
	}
	return p, fa
}
