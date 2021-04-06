package daemon

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dbus"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/tun"
)

func (o *outbound) tryResolveD(c context.Context) error {
	// Connect to ResolveD via DBUS.
	dConn, err := dbus.NewResolveD()
	if err != nil {
		return errResolveDNotConfigured
	}
	defer func() {
		_ = dConn.Close()
	}()

	if !dConn.IsRunning() {
		return errResolveDNotConfigured
	}

	// Create a new local address that the DNS resolver can listen to.
	dnsResolverListener, err := net.ListenPacket("udp", "127.0.0.1:")
	if err != nil {
		return errResolveDNotConfigured
	}
	dnsResolverAddr, err := splitToUDPAddr(dnsResolverListener.LocalAddr())
	if err != nil {
		return errResolveDNotConfigured
	}

	dlog.Info(c, "systemd-resolved is running")
	t, err := tun.CreateInterfaceWithDNS(c, dConn)
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
		namespaces[tel2SubDomain] = struct{}{}

		o.domainsLock.Lock()
		o.namespaces = namespaces
		o.search = search
		o.domainsLock.Unlock()
		err := dConn.SetLinkDomains(t.InterfaceIndex(), paths...)
		if err != nil {
			dlog.Errorf(c, "failed to revert virtual interface link: %v", err)
		}
	}

	c, cancel := context.WithCancel(c)
	defer cancel()

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Closer", func(c context.Context) error {
		<-c.Done()
		dlog.Infof(c, "Reverting link %s", t.Name())
		if err := dConn.RevertLink(t.InterfaceIndex()); err != nil {
			dlog.Errorf(c, "failed to revert virtual interface link: %v", err)
		}
		_ = t.Close() // This will terminate the ForwardDNS read loop gracefully
		return nil
	})

	// DNS resolver
	g.Go("Server", func(c context.Context) error {
		v := dns.NewServer(c, []net.PacketConn{dnsResolverListener}, "", o.resolveNoSearch)
		return v.Run(c)
	})
	initDone := &sync.WaitGroup{}
	initDone.Add(1)
	g.Go("Forwarder", func(c context.Context) error {
		return t.ForwardDNS(c, dnsResolverAddr, initDone)
	})
	g.Go("SanityCheck", func(c context.Context) error {
		initDone.Wait()

		// Check if an attempt to resolve a DNS address reaches our DNS resolver, 300ms should be plenty

		cmdC, cmdCancel := context.WithTimeout(c, 300*time.Millisecond)
		defer cmdCancel()
		_, _ = net.DefaultResolver.LookupHost(cmdC, "jhfweoitnkgyeta")
		if t.RequestCount() == 0 {
			return errResolveDNotConfigured
		}
		return nil
	})
	return g.Wait()
}
