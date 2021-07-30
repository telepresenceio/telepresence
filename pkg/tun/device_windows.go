package tun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

// This device will require that wintun.dll is available to the loader.
// See: https://www.wintun.net/ for more info.
type Device struct {
	tun.Device
	name           string
	dns            net.IP
	interfaceIndex uint32
}

func openTun(ctx context.Context) (td *Device, err error) {
	defer func() {
		if r := recover(); r != nil {
			var ok bool
			if err, ok = r.(error); !ok {
				err = derror.PanicToError(r)
			}
		}
	}()
	interfaceName := "tel0"
	td = &Device{}
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
	td.interfaceIndex = iface.InterfaceIndex

	return td, nil
}

func (t *Device) Close() error {
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

func (t *Device) getLUID() winipcfg.LUID {
	return winipcfg.LUID(t.Device.(*tun.NativeTun).LUID())
}

func (t *Device) addSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().AddIPAddress(*subnet)
}

func (t *Device) removeSubnet(_ context.Context, subnet *net.IPNet) error {
	return t.getLUID().DeleteIPAddress(*subnet)
}

func (t *Device) setDNS(ctx context.Context, server net.IP, domains []string) (err error) {
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
	if err = luid.SetDNS(family, []net.IP{server}, domains); err != nil {
		return err
	}

	// On some systems, SetDNS isn't enough to allow the domains to be resolved, and the network adapter's domain has to be set explicitly.
	// It's actually way easier to do this via powershell than any system calls that can be run from go code
	domain := ""
	if len(domains) > 0 {
		// Quote the domain to prevent powershell injection
		domain = shellquote.ShellArgsString([]string{strings.TrimSuffix(domains[0], ".")})
	}
	// It's apparently well known that WMI queries can hang under various conditions, so we add a timeout here to prevent hanging the daemon
	// Fun fact: terminating the context that powershell is running in will not stop a hanging WMI call (!) perhaps because it is considered uninterruptible
	pshScript := fmt.Sprintf(`
$job = Get-WmiObject Win32_NetworkAdapterConfiguration -filter "interfaceindex='%d'" -AsJob | Wait-Job -Timeout 30
if ($job.State -ne 'Completed') {
	throw "timed out getting network adapter after 30 seconds."
}
$obj = $job | Receive-Job
$job = Invoke-WmiMethod -InputObject $obj -Name SetDNSDomain -ArgumentList "%s" -AsJob | Wait-Job -Timeout 30
if ($job.State -ne 'Completed') {
	throw "timed out setting network adapter DNS Domain after 30 seconds."
}
$job | Receive-Job
`, t.interfaceIndex, domain)
	cmd := dexec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", pshScript)
	cmd.DisableLogging = true // disable chatty logging
	dlog.Debugf(ctx, "Calling powershell's SetDNSDomain %q", domain)
	if err := cmd.Run(); err != nil {
		// Log the error, but don't actually fail on it: This is all just a fallback for SetDNS, so the domains might actually be working
		dlog.Errorf(ctx, "Failed to set NetworkAdapterConfiguration DNS Domain: %v. Will proceed, but namespace mapping might not be functional.", err)
	}

	dlog.Debug(ctx, "Calling ipconfig /flushdns")
	cmd = dexec.CommandContext(ctx, "ipconfig", "/flushdns")
	cmd.DisableLogging = true
	_ = cmd.Run()
	t.dns = server
	return nil
}

func (t *Device) setMTU(mtu int) error {
	return errors.New("not implemented")
}

func (t *Device) readPacket(into *buffer.Data) (int, error) {
	return t.Device.Read(into.Raw(), 0)
}

func (t *Device) writePacket(from *buffer.Data) (int, error) {
	return t.Device.Write(from.Raw(), 0)
}
