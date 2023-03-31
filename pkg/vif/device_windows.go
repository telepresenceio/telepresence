package vif

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

// This nativeDevice will require that wintun.dll is available to the loader.
// See: https://www.wintun.net/ for more info.
type nativeDevice struct {
	tun.Device
	name           string
	dns            net.IP
	interfaceIndex int32
}

func openTun(ctx context.Context) (td *nativeDevice, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = derror.PanicToError(r)
			dlog.Errorf(ctx, "%+v", err)
		}
	}()
	interfaceName := "tel0"
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
	conn, err := net.Dial("udp", t.dns.String()+":53")
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

func addrFromIP(ip net.IP) netip.Addr {
	var addr netip.Addr
	if ip4 := ip.To4(); ip4 != nil {
		addr = netip.AddrFrom4(*(*[4]byte)(ip4))
	} else if ip16 := ip.To16(); ip16 != nil {
		addr = netip.AddrFrom16(*(*[16]byte)(ip16))
	}
	return addr
}

func prefixFromIPNet(subnet *net.IPNet) netip.Prefix {
	if subnet == nil {
		return netip.Prefix{}
	}
	ones, _ := subnet.Mask.Size()
	return netip.PrefixFrom(addrFromIP(subnet.IP), ones)
}

func (t *nativeDevice) addSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().AddIPAddress(prefixFromIPNet(subnet))
}

func (t *nativeDevice) removeSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().DeleteIPAddress(prefixFromIPNet(subnet))
}

func (t *nativeDevice) setDNS(ctx context.Context, clusterDomain string, server net.IP, searchList []string) (err error) {
	// This function must not be interrupted by a context cancellation, so we give it a timeout instead.
	parentCtx := ctx
	ctx, cancel := context.WithCancel(dcontext.WithoutCancel(ctx))
	go func() {
		<-parentCtx.Done()
		// Give this function some time to complete its task. Configuring DSN on windows is slow.
		time.Sleep(10 * time.Second)
		cancel()
	}()

	ipFamily := func(ip net.IP) winipcfg.AddressFamily {
		f := winipcfg.AddressFamily(windows.AF_INET6)
		if ip4 := ip.To4(); ip4 != nil {
			f = windows.AF_INET
		}
		return f
	}
	family := ipFamily(server)
	luid := t.getLUID()
	if t.dns != nil {
		if oldFamily := ipFamily(t.dns); oldFamily != family {
			_ = luid.FlushDNS(oldFamily)
		}
	}
	serverStr := server.String()
	servers16, err := windows.UTF16PtrFromString(serverStr)
	if err != nil {
		return err
	}
	searchList16, err := windows.UTF16PtrFromString(strings.Join(searchList, ","))
	if err != nil {
		return err
	}
	guid, err := luid.GUID()
	if err != nil {
		return err
	}
	dnsInterfaceSettings := &winipcfg.DnsInterfaceSettings{
		Version:    winipcfg.DnsInterfaceSettingsVersion1,
		Flags:      winipcfg.DnsInterfaceSettingsFlagNameserver | winipcfg.DnsInterfaceSettingsFlagSearchList,
		NameServer: servers16,
		SearchList: searchList16,
	}
	if family == windows.AF_INET6 {
		dnsInterfaceSettings.Flags |= winipcfg.DnsInterfaceSettingsFlagIPv6
	}
	if err = winipcfg.SetInterfaceDnsSettings(*guid, dnsInterfaceSettings); err != nil {
		return err
	}

	// Unless we also update the global DNS search path, the one for the device doesn't work on some platforms.
	// This behavior is mainly observed on Windows Server editions.

	// Retrieve the current global search paths so that paths that aren't related to
	// the cluster domain (i.e. not managed by us) can be retained.
	gss, err := getGlobalSearchList()
	if err != nil {
		return err
	}
	if oldLen := len(gss); oldLen > 0 {
		// Windows does not use a dot suffix in the search path.
		clusterDomain = strings.TrimSuffix(clusterDomain, ".")

		// Put our new search path in front of other entries. Then include those
		// that don't end with our cluster domain (these are entries that aren't
		// managed by Telepresence).
		newGss := make([]string, len(searchList), oldLen)
		copy(newGss, searchList)
		for _, gs := range gss {
			if !strings.HasSuffix(gs, clusterDomain) {
				newGss = append(newGss, gs)
			}
		}
		gss = newGss
	} else {
		gss = searchList
	}
	t.dns = server
	return setGlobalSearchList(ctx, gss)
}

func psList(values []string) string {
	var sb strings.Builder
	sb.WriteString("@(")
	for i, gs := range values {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		sb.WriteString(strings.TrimSuffix(gs, "."))
		sb.WriteByte('"')
	}
	sb.WriteByte(')')
	return sb.String()
}

func getGlobalSearchList() ([]string, error) {
	rk, err := registry.OpenKey(registry.LOCAL_MACHINE, `System\CurrentControlSet\Services\Tcpip\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return nil, err
	}
	csv, _, err := rk.GetStringValue("SearchList")
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return nil, err
	}
	if csv == "" {
		return nil, nil
	}
	return strings.Split(csv, ","), nil
}

func setGlobalSearchList(ctx context.Context, gss []string) error {
	cmd := proc.CommandContext(ctx, "powershell.exe", "Set-DnsClientGlobalSetting", "-SuffixSearchList", psList(gss))
	_, err := proc.CaptureErr(ctx, cmd)
	return err
}

func (t *nativeDevice) setMTU(int) error {
	return errors.New("not implemented")
}

func (t *nativeDevice) readPacket(into *buffer.Data) (int, error) {
	return t.Device.Read(into.Raw(), 0)
}

func (t *nativeDevice) writePacket(from *buffer.Data, offset int) (int, error) {
	return t.Device.Write(from.Raw(), offset)
}
