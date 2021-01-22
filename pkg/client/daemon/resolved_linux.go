package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/pkg/client/daemon/dbus"
	"github.com/datawire/telepresence2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/pkg/client/daemon/tun"
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
	dnsResolverAddr, err := dnsResolverAddr()
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
	initDone := &sync.WaitGroup{}
	initDone.Add(2)
	g.Go("Server", func(c context.Context) error {
		v := dns.NewServer(c, []string{dnsResolverAddr.String()}, "", func(domain string) string {
			// Namespaces are defined on the network DNS config and managed by ResolveD, so not needed here.
			if r := o.resolveNoNS(domain); r != nil {
				return r.Ip
			}
			return ""
		})
		return v.Run(c, initDone)
	})
	g.Go("Forwarder", func(c context.Context) error {
		return t.ForwardDNS(c, dnsResolverAddr, initDone)
	})
	g.Go(proxyWorker, func(c context.Context) error {
		initDone.Wait()

		// Check if an attempt to resolve a DNS address reaches our DNS resolver, 300ms should be plenty
		cmdC, cmdCancel := context.WithTimeout(c, 300*time.Millisecond)
		defer cmdCancel()
		_ = dexec.CommandContext(cmdC, "nslookup", "jhfweoitnkgyeta").Run()
		if t.RequestCount() == 0 {
			return errResolveDNotConfigured
		}
		return o.proxyWorker(c)
	})
	return g.Wait()
}
