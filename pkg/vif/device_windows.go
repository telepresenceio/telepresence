package vif

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

// This nativeDevice will require that wintun.dll is available to the loader.
// See: https://www.wintun.net/ for more info.
type nativeDevice struct {
	tun.Device
	name           string
	dns            netip.Addr
	interfaceIndex int32
}

func openTun(ctx context.Context) (td *nativeDevice, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = derror.PanicToError(r)
			dlog.Errorf(ctx, "%+v", err)
		}
	}()
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
	td = &nativeDevice{}
	if td.Device, err = tun.CreateTUN(interfaceName, 0); err != nil {
		return nil, fmt.Errorf("failed to create TUN device: %w", err)
	}
	if td.name, err = td.Device.Name(); err != nil {
		return nil, fmt.Errorf("failed to get real name of TUN device: %w", err)
	}
	iface, err := td.getLUID().Interface()
	if err != nil {
		return nil, fmt.Errorf("failed to get interface for TUN device: %w", err)
	}
	td.interfaceIndex = int32(iface.InterfaceIndex)

	return td, nil
}

func (t *nativeDevice) Close() error {
	// The tun.NativeTun device has a closing mutex which is read locked during
	// a call to Read(). The read lock prevents a call to Close() to proceed
	// until Read() actually receives something. To resolve that "deadlock",
	// we call Close() in one goroutine to wait for the lock and write a bogus
	// message in another that will be returned by Read().
	closeCh := make(chan error)
	go func() {
		// first message is just to indicate that this goroutine has started
		closeCh <- nil
		closeCh <- t.Device.Close()
		close(closeCh)
	}()

	// Not 100%, but we can be fairly sure that Close() is
	// hanging on the lock, or at least will be by the time
	// the Read() returns
	<-closeCh

	// Send something to the TUN device so that the Read
	// unlocks the NativeTun.closing mutex and let the actual
	// Close call continue
	conn, err := net.Dial("udp", net.JoinHostPort(t.dns.String(), "53"))
	if err == nil {
		_, _ = conn.Write([]byte("bogus"))
	}
	return <-closeCh
}

func (t *nativeDevice) getLUID() winipcfg.LUID {
	return winipcfg.LUID(t.Device.(*tun.NativeTun).LUID())
}

func (t *nativeDevice) index() int32 {
	return t.interfaceIndex
}

func (t *nativeDevice) addSubnet(_ context.Context, subnet netip.Prefix) error {
	return t.getLUID().AddIPAddress(subnet)
}

func (t *nativeDevice) removeSubnet(_ context.Context, subnet netip.Prefix) error {
	return t.getLUID().DeleteIPAddress(subnet)
}

func (t *nativeDevice) setDNS(ctx context.Context, clusterDomain string, server netip.Addr, searchList []string) (err error) {
	// This function must not be interrupted by a context cancellation, so we give it a timeout instead.
	dlog.Debugf(ctx, "SetDNS server: %s, searchList: %v, domain: %q", server, searchList, clusterDomain)
	defer dlog.Debug(ctx, "SetDNS done")

	luid := t.getLUID()
	family := addressFamily(server)
	if t.dns.IsValid() {
		if oldFamily := addressFamily(t.dns); oldFamily != family {
			_ = luid.FlushDNS(oldFamily)
		}
	}
	t.dns = server
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
	return luid.SetDNS(family, []netip.Addr{t.dns}, searchList)
}

func addressFamily(ip netip.Addr) winipcfg.AddressFamily {
	f := winipcfg.AddressFamily(windows.AF_INET6)
	if ip.Is4() {
		f = windows.AF_INET
	}
	return f
}

func (t *nativeDevice) setMTU(int) error {
	return errors.New("not implemented")
}

func (t *nativeDevice) readPacket(into *buffer.Data) (int, error) {
	sz := make([]int, 1)
	packetsN, err := t.Device.Read([][]byte{into.Raw()}, sz, 0)
	if err != nil {
		return 0, err
	}
	if packetsN == 0 {
		return 0, io.EOF
	}
	return sz[0], nil
}

func (t *nativeDevice) writePacket(from *buffer.Data, offset int) (int, error) {
	packetsN, err := t.Device.Write([][]byte{from.Raw()}, offset)
	if err != nil {
		return 0, err
	}
	if packetsN == 0 {
		return 0, io.EOF
	}
	return len(from.Raw()), nil
}
