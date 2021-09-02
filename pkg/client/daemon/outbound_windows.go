package daemon

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
)

func (o *outbound) dnsServerWorker(c context.Context) error {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return err
	}
	o.router.configureDNS(c, dnsAddr)

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		select {
		case <-c.Done():
			return nil
		case <-o.router.configured():
			o.processSearchPaths(g, o.updateRouterDNS)
			v := dns.NewServer(c, []net.PacketConn{listener}, nil, o.resolveInCluster)
			return v.Run(c)
		}
	})
	return g.Wait()
}

func (o *outbound) updateRouterDNS(c context.Context, paths []string) error {
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
	o.domainsLock.Lock()
	o.namespaces = namespaces
	o.search = search
	o.domainsLock.Unlock()
	err := o.router.dev.SetDNS(c, o.router.dnsIP, search)
	if err != nil {
		return fmt.Errorf("failed to set DNS: %w", err)
	}
	return nil
}
