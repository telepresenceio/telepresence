package daemon

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
)

func (s *session) dnsServerWorker(c context.Context) error {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return err
	}
	s.configureDNS(c, dnsAddr)

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		select {
		case <-c.Done():
			return nil
		case <-s.configured():
			s.processSearchPaths(g, s.updateRouterDNS)
			return dns.NewServer([]net.PacketConn{listener}, nil, s.resolveInCluster, &s.dnsCache).Run(c, make(chan struct{}))
		}
	})
	return g.Wait()
}

func (s *session) updateRouterDNS(c context.Context, paths []string) error {
	namespaces := make(map[string]struct{})
	search := make([]string, 0)
	for _, path := range paths {
		if strings.ContainsRune(path, '.') {
			search = append(search, path)
		} else if path != "" {
			namespaces[path] = struct{}{}
		}
	}
	namespaces[tel2SubDomain] = struct{}{}
	s.domainsLock.Lock()
	s.namespaces = namespaces
	s.search = search
	s.domainsLock.Unlock()
	err := s.dev.SetDNS(c, s.dnsIP, search)
	s.flushDNS()
	if err != nil {
		return fmt.Errorf("failed to set DNS: %w", err)
	}
	return nil
}
