package dns

import (
	"context"
	"fmt"
	"net"
	"strings"
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
	configureDNS(s.config.RemoteIp, dnsAddr)

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		s.processSearchPaths(g, s.updateRouterDNS, dev)
		return s.Run(c, make(chan struct{}), []net.PacketConn{listener}, nil, s.resolveInCluster)
	})
	return g.Wait()
}

func (s *Server) updateRouterDNS(c context.Context, paths []string, dev vif.Device) error {
	namespaces := make(map[string]struct{})
	search := make([]string, 0)
	for _, path := range paths {
		if strings.ContainsRune(path, '.') {
			search = append(search, path)
		} else if path != "" {
			namespaces[path] = struct{}{}
		}
	}
	s.domainsLock.Lock()
	s.namespaces = namespaces
	s.search = search
	s.domainsLock.Unlock()
	err := dev.SetDNS(c, s.clusterDomain, s.config.RemoteIp, search)
	s.flushDNS()
	if err != nil {
		return fmt.Errorf("failed to set DNS: %w", err)
	}
	return nil
}
