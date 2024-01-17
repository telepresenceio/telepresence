package dns

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 40
	recursionTestTimeout    = 1500 * time.Millisecond
)

func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return err
	}
	configureDNS(s.remoteIP, dnsAddr)

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		defer func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
			s.Lock()
			_ = dev.SetDNS(c, s.clusterDomain, s.remoteIP, nil)
			s.Unlock()
			cancel()
		}()
		if err := s.updateRouterDNS(c, dev); err != nil {
			return err
		}
		s.processSearchPaths(g, s.updateRouterDNS, dev)
		return s.Run(c, make(chan struct{}), []net.PacketConn{listener}, nil, s.resolveInCluster)
	})
	return g.Wait()
}

func (s *Server) updateRouterDNS(c context.Context, dev vif.Device) error {
	s.Lock()
	err := dev.SetDNS(c, s.clusterDomain, s.remoteIP, s.search)
	s.Unlock()
	s.flushDNS()
	if err != nil {
		return fmt.Errorf("failed to set DNS: %w", err)
	}
	return nil
}
