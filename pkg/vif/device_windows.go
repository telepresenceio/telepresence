package vif

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/dlib/v2/dlog"
)

// This device will require that wintun.dll is available to the loader.
// See: https://www.wintun.net/ for more info.
type device struct {
	*channel.Endpoint
	dev            tun.Device
	ctx            context.Context
	name           string
	dns            netip.AddrPort
	interfaceIndex uint32
	luid           winipcfg.LUID
	wb             bytes.Buffer
	wg             sync.WaitGroup
}

func openTun(ctx context.Context) (td *device, err error) {
	interfaceFmt := "tel%d"
	ifaceNumber := 0
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}
	for _, iface := range ifaces {
		dlog.Tracef(ctx, "Found interface %s", iface.Name)
		// Parse the tel%d number if it's there
		var num int
		if _, err := fmt.Sscanf(iface.Name, interfaceFmt, &num); err == nil {
			if num >= ifaceNumber {
				ifaceNumber = num + 1
			}
		}
	}
	interfaceName := fmt.Sprintf(interfaceFmt, ifaceNumber)

	dlog.Infof(ctx, "Creating interface %s", interfaceName)
	dev, err := tun.CreateTUN(interfaceName, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN device: %w", err)
	}
	name, err := dev.Name()
	if err != nil {
		return nil, fmt.Errorf("failed to get real name of TUN device: %w", err)
	}
	luid := winipcfg.LUID(dev.(*tun.NativeTun).LUID())
	iface, err := luid.Interface()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface for TUN device: %w", err)
	}

	mtu, err := dev.MTU()
	if err != nil {
		return nil, fmt.Errorf("failed to get MTU for TUN device: %w", err)
	}
	if mtu < 1500 {
		mtu = 1500
	}
	dlog.Debugf(ctx, "using MTU = %d", mtu)
	return &device{
		Endpoint:       channel.New(defaultDevOutQueueLen, uint32(mtu), ""),
		dev:            dev,
		ctx:            ctx,
		name:           name,
		interfaceIndex: iface.InterfaceIndex,
		luid:           luid,
	}, nil
}

// Close closes both the Device and the Endpoint. This function overrides the LinkEndpoint.Close so
// it cannot return an error.
func (d *device) Close() {
	d.Endpoint.Close()

	// The tun.NativeTun device has a closing mutex which is read locked during
	// a call to Read(). The read lock prevents a call to Close() to proceed
	// until Read() actually receives something. To resolve that "deadlock",
	// we call Close() in one goroutine to wait for the lock and write a bogus
	// message in another that will be returned by Read().
	closeCh := make(chan error)
	go func() {
		// first message is just to indicate that this goroutine has started
		closeCh <- nil
		closeCh <- d.dev.Close()
		close(closeCh)
	}()

	// Not 100%, but we can be fairly sure that Close() is
	// hanging on the lock, or at least will be by the time
	// the Read() returns
	<-closeCh

	// Send something to the TUN device so that the Read
	// unlocks the NativeTun.closing mutex and let the actual
	// Close call continue
	conn, err := net.Dial("udp", net.JoinHostPort(d.dns.String(), "53"))
	if err == nil {
		_, _ = conn.Write([]byte("bogus"))
	}
	<-closeCh
}

func (d *device) getLUID() winipcfg.LUID {
	return winipcfg.LUID(d.dev.(*tun.NativeTun).LUID())
}

func (d *device) addSubnet(_ context.Context, subnet netip.Prefix) error {
	return d.getLUID().AddIPAddress(subnet)
}

func (d *device) removeSubnet(_ context.Context, subnet netip.Prefix) error {
	return d.getLUID().DeleteIPAddress(subnet)
}

func (d *device) setDNS(ctx context.Context, clusterDomain string, server netip.AddrPort, searchList []string) (err error) {
	// This function must not be interrupted by a context cancellation, so we give it a timeout instead.
	dlog.Debugf(ctx, "SetDNS server: %s, searchList: %v, domain: %q", server, searchList, clusterDomain)
	defer dlog.Debug(ctx, "SetDNS done")

	luid := d.getLUID()
	family := addressFamily(server.Addr())
	if d.dns.IsValid() {
		if oldFamily := addressFamily(d.dns.Addr()); oldFamily != family {
			_ = luid.FlushDNS(oldFamily)
		}
	}
	d.dns = server
	clusterDomain = strings.TrimSuffix(clusterDomain, ".")
	cdi := slices.Index(searchList, clusterDomain)
	switch cdi {
	case 0:
		// clusterDomain is already in first position
	case -1:
		// clusterDomain is not included in the list
		searchList = slices.Insert(searchList, 0, clusterDomain)
	default:
		// put clusterDomain first in list, but retain the order of remaining elements
		searchList = slices.Insert(slices.Delete(searchList, cdi, cdi+1), 0, clusterDomain)
	}
	return luid.SetDNS(family, []netip.Addr{d.dns.Addr()}, searchList)
}

func addressFamily(ip netip.Addr) winipcfg.AddressFamily {
	f := winipcfg.AddressFamily(windows.AF_INET6)
	if ip.Is4() {
		f = windows.AF_INET
	}
	return f
}

func (d *device) headerSkip() int {
	return 0
}

func (d *device) readPacket(buf []byte) (int, error) {
	sz := make([]int, 1)
	packetsN, err := d.dev.Read([][]byte{buf}, sz, 0)
	if err != nil {
		return 0, err
	}
	if packetsN == 0 {
		return 0, io.EOF
	}
	return sz[0], nil
}

func (d *device) writePacket(from *stack.PacketBuffer) error {
	wb := &d.wb
	wb.Reset()
	for _, s := range from.AsSlices() {
		wb.Write(s)
	}
	packetsN, err := d.dev.Write([][]byte{wb.Bytes()}, 0)
	if err != nil {
		return err
	}
	if packetsN == 0 {
		return io.EOF
	}
	return nil
}
