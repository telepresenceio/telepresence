package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dbus"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

func (s *Server) tryResolveD(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	// Connect to ResolveD via DBUS.
	if !dbus.IsResolveDRunning(c) {
		dlog.Error(c, "systemd-resolved is not running")
		return errResolveDNotConfigured
	}

	c, cancelResolveD := context.WithCancel(c)
	defer cancelResolveD()

	listeners, err := s.dnsListeners(c)
	if err != nil {
		return err
	}
	// Create a new local address that the DNS resolver can listen to.
	dnsResolverAddr, err := splitToUDPAddr(listeners[0].LocalAddr())
	if err != nil {
		return err
	}
	dnsIP := s.remoteIP
	configureDNS(dnsIP, dnsResolverAddr)

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	// DNS resolver
	initDone := make(chan struct{})

	g.Go("Server", func(c context.Context) error {
		dlog.Infof(c, "Configuring DNS IP %s", dnsIP)
		if err = dbus.SetLinkDNS(c, int(dev.Index()), dnsIP); err != nil {
			dlog.Error(c, err)
			initDone <- struct{}{}
			return errResolveDNotConfigured
		}
		defer func() {
			// It's very likely that the context is cancelled here. We use it
			// anyway, stripped from cancellation, to retain logging.
			c, cancel := context.WithTimeout(context.WithoutCancel(c), time.Second)
			defer cancel()
			dlog.Debugf(c, "Reverting Link settings for %s", dev.Name())
			configureDNS(nil, nil) // Don't route from TUN-device
			if err = dbus.RevertLink(c, int(dev.Index())); err != nil {
				dlog.Error(c, err)
			}
			// No need to close listeners here. They are closed by the dnsServer
		}()
		if err = s.updateLinkDomains(c, dev); err != nil {
			dlog.Error(c, err)
			initDone <- struct{}{}
			return errResolveDNotConfigured
		}
		return s.Run(c, initDone, listeners, nil, s.resolveInCluster)
	})

	g.Go("SanityCheck", func(c context.Context) error {
		if _, ok := <-initDone; ok {
			// initDone was not closed, bail out.
			return errResolveDNotConfigured
		}

		// Check if an attempt to resolve a DNS address reaches our DNS resolver, Two seconds should be plenty
		cmdC, cmdCancel := context.WithTimeout(c, 2*time.Second)
		defer cmdCancel()
		for cmdC.Err() == nil {
			go func() {
				dlog.Debug(cmdC, "sanity-check lookup")
				_, _ = net.DefaultResolver.LookupHost(cmdC, santiyCheck)
				if s.RequestCount() > 0 {
					cmdCancel()
				}
			}()
			dtime.SleepWithContext(cmdC, 200*time.Millisecond)
		}
		<-cmdC.Done()
		if s.RequestCount() > 0 {
			// The query went all way through. Start processing search paths systemd-resolved style
			// and return nil for successful validation.
			s.processSearchPaths(g, s.updateLinkDomains, dev)
			return nil
		}
		s.flushDNS()
		dlog.Error(c, "resolver did not receive requests from systemd-resolved")
		return errResolveDNotConfigured
	})
	return g.Wait()
}

func (s *Server) updateLinkDomains(c context.Context, dev vif.Device) error {
	s.Lock()
	paths := make([]string, len(s.search)+len(s.routes)+len(s.includeSuffixes)+1)

	// Namespaces are copied verbatim. Entries that aren't prefixed with "~" are considered search path entries.
	copy(paths, s.search)
	i := len(s.search)
	for ns := range s.routes {
		paths[i] = "~" + ns
		i++
	}

	// Include-suffixes are routes, i.e. in contrast to search paths, they are never appended to the name, but
	// used as a filter that will direct queries for names ending with them to this resolver. Routes must be
	// prefixed with "~".
	for _, sfx := range s.includeSuffixes {
		paths[i] = "~" + strings.TrimPrefix(sfx, ".")
		i++
	}
	paths[i] = "~" + s.clusterDomain
	s.Unlock()

	if err := dbus.SetLinkDomains(dcontext.HardContext(c), int(dev.Index()), paths...); err != nil {
		return fmt.Errorf("failed to set link domains on %q: %w", dev.Name(), err)
	}
	s.flushDNS()
	dlog.Debugf(c, "Link domains on device %q set to [%s]", dev.Name(), strings.Join(paths, ","))
	return nil
}
