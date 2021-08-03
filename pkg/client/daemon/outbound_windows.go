package daemon

import (
	"context"
	"net"
	"strings"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
)

func (o *outbound) dnsServerWorker(c context.Context) error {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	o.setSearchPathFunc = func(c context.Context, paths []string) {
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
			dlog.Errorf(c, "failed to set DNS: %v", err)
		}
	}

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		select {
		case <-c.Done():
			close(o.dnsConfigured)
			return nil
		case dnsIP := <-o.kubeDNS:
			addr := listener.LocalAddr()
			o.router.configureDNS(c, dnsIP, uint16(53), addr.(*net.UDPAddr))
			close(o.dnsConfigured)
			v := dns.NewServer(c, []net.PacketConn{listener}, nil, o.resolveInCluster)
			return v.Run(c)
		}
	})
	return g.Wait()
}
