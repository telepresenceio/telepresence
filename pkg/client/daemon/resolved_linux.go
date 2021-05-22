package daemon

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dbus"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
)

func (o *outbound) tryResolveD(c context.Context, dev *tun.Device) error {
	// Connect to ResolveD via DBUS.
	dConn, err := dbus.NewResolveD()
	if err != nil {
		dlog.Error(c, err)
		return errResolveDNotConfigured
	}
	defer func() {
		_ = dConn.Close()
	}()

	if !dConn.IsRunning() {
		dlog.Error(c, "systemd.resolved is not running")
		return errResolveDNotConfigured
	}

	// Create a new local address that the DNS resolver can listen to.
	dnsResolverListener, err := net.ListenPacket("udp", "127.0.0.1:")
	if err != nil {
		dlog.Error(c, err)
		return errResolveDNotConfigured
	}
	dnsResolverAddr, err := splitToUDPAddr(dnsResolverListener.LocalAddr())
	if err != nil {
		dlog.Error(c, err)
		return errResolveDNotConfigured
	}

	o.setSearchPathFunc = func(c context.Context, paths []string) {
		// When using systemd.resolved, we provide resolution of NAME.NAMESPACE by adding each
		// namespace as a route (a search entry prefixed with ~)
		namespaces := make(map[string]struct{})
		search := make([]string, 0)
		for i, path := range paths {
			if strings.ContainsRune(path, '.') {
				search = append(search, path)
			} else {
				namespaces[path] = struct{}{}
				// Turn namespace into a route
				paths[i] = "~" + path
			}
		}
		paths = append(paths, kubernetesZone+".")
		namespaces[tel2SubDomain] = struct{}{}

		o.domainsLock.Lock()
		o.namespaces = namespaces
		o.search = search
		o.domainsLock.Unlock()
		if err := dConn.SetLinkDomains(int(dev.Index()), paths...); err != nil {
			dlog.Errorf(c, "failed to set link domains on %q: %v", dev.Name(), err)
		} else {
			dlog.Debugf(c, "Link domains on device %q set to [%s]", dev.Name(), strings.Join(paths, ","))
		}
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	// DNS resolver
	initDone := make(chan struct{})

	var dnsServer *dns.Server
	g.Go("Server", func(c context.Context) error {
		select {
		case <-c.Done():
			initDone <- struct{}{}
			return nil
		case dnsIP := <-o.kubeDNS:
			dlog.Infof(c, "Configuring DNS IP %s", dnsIP)
			if err = dConn.SetLinkDNS(int(dev.Index()), dnsIP); err != nil {
				dlog.Error(c, err)
				initDone <- struct{}{}
				return errResolveDNotConfigured
			}
			dnsServer = dns.NewServer(c, []net.PacketConn{dnsResolverListener}, nil, o.resolveNoSearch)
			if err = o.router.configureDNS(c, dnsIP, uint16(53), dnsResolverAddr); err != nil {
				dlog.Error(c, err)
				initDone <- struct{}{}
				return err
			}
			close(initDone)
			return dnsServer.Run(c)
		}
	})
	g.Go("SanityCheck", func(c context.Context) error {
		if _, ok := <-initDone; ok {
			// initDonw was not closed, bail out.
			return errResolveDNotConfigured
		}

		// Check if an attempt to resolve a DNS address reaches our DNS resolver, 300ms should be plenty

		cmdC, cmdCancel := context.WithTimeout(c, 300*time.Millisecond)
		defer cmdCancel()
		for cmdC.Err() == nil {
			_, _ = net.DefaultResolver.LookupHost(cmdC, "jhfweoitnkgyeta."+tel2SubDomain)
			if dnsServer.RequestCount() > 0 {
				close(o.dnsConfigured)
				return nil
			}
			dtime.SleepWithContext(cmdC, 30*time.Millisecond)
		}
		dlog.Error(c, "resolver did not receive requests from systemd.resolved")
		return errResolveDNotConfigured
	})
	return g.Wait()
}
