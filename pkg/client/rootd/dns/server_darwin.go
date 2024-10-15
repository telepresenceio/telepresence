package dns

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 10
	recursionTestTimeout    = 500 * time.Millisecond
)

// Worker places a file under the /etc/resolver directory so that it is picked up by the
// macOS resolver. The file is configured with a single nameserver that points to the local IP
// that the Telepresence DNS server listens to. The file is removed, and the DNS is flushed when
// the worker terminates
//
// For more information about /etc/resolver files, please view the man pages available at
//
//	man 5 resolver
//
// or, if not on a Mac, follow this link: https://www.manpagez.com/man/5/resolver/
func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	resolverDirName := filepath.Join("/etc", "resolver")

	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return err
	}
	configureDNS(nil, dnsAddr)

	err = os.MkdirAll(resolverDirName, 0o755)
	if err != nil {
		return err
	}

	// Ensure lingering all telepresence.* files are removed.
	if err := s.removeResolverFiles(c, resolverDirName); err != nil {
		return err
	}

	defer func() {
		_ = s.removeResolverFiles(c, resolverDirName)
		s.flushDNS()
	}()

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		if err := s.updateResolverFiles(c, resolverDirName, dnsAddr); err != nil {
			return err
		}
		s.processSearchPaths(g, func(c context.Context, _ vif.Device) error {
			return s.updateResolverFiles(c, resolverDirName, dnsAddr)
		}, dev)
		// Server will close the listener, so no need to close it here.
		return s.Run(c, make(chan struct{}), []net.PacketConn{listener}, nil, s.resolveInCluster)
	})
	return g.Wait()
}

// removeResolverFiles performs rm -f /etc/resolver/telepresence.*.
func (s *Server) removeResolverFiles(c context.Context, resolverDirName string) error {
	files, err := os.ReadDir(resolverDirName)
	if err != nil {
		return err
	}
	for _, file := range files {
		if n := file.Name(); strings.HasPrefix(n, "telepresence.") {
			fn := filepath.Join(resolverDirName, n)
			dlog.Debugf(c, "Removing file %q", fn)
			if err := os.Remove(fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) updateResolverFiles(c context.Context, resolverDirName string, dnsAddr *net.UDPAddr) error {
	s.Lock()
	defer s.Unlock()

	nameservers := []string{dnsAddr.IP.String()}
	port := dnsAddr.Port
	newDomainResolveFile := func(domain string) *dnsproxy.ResolveFile {
		return &dnsproxy.ResolveFile{
			Port:        port,
			Domain:      domain,
			Nameservers: nameservers,
		}
	}

	// All routes and include suffixes become domains
	domains := make(map[string]*dnsproxy.ResolveFile, len(s.routes)+len(s.IncludeSuffixes))
	for route := range s.routes {
		domains[route] = newDomainResolveFile(route)
	}
	for _, sfx := range s.IncludeSuffixes {
		sfx = strings.TrimPrefix(sfx, ".")
		domains[sfx] = newDomainResolveFile(sfx)
	}
	clusterDomain := strings.TrimSuffix(s.clusterDomain, ".")
	domains[clusterDomain] = newDomainResolveFile(clusterDomain)
	domains[tel2SubDomain] = newDomainResolveFile(tel2SubDomain)

nextSearch:
	for _, search := range s.search {
		search = strings.TrimSuffix(search, ".")
		if df, ok := domains[search]; ok {
			df.Search = append(df.Search, search)
			continue
		}
		for domain, df := range domains {
			if strings.HasSuffix(search, "."+domain) {
				df.Search = append(df.Search, search)
				continue nextSearch
			}
		}
	}

	for domain := range s.domains {
		if _, ok := domains[domain]; !ok {
			nsFile := domainResolverFile(resolverDirName, domain)
			dlog.Infof(c, "Removing %s", nsFile)
			if err := os.Remove(nsFile); err != nil {
				dlog.Error(c, err)
			}
			delete(s.domains, domain)
		}
	}

	for domain, rf := range domains {
		nsFile := domainResolverFile(resolverDirName, domain)
		if _, ok := s.domains[domain]; ok {
			if oldRf, err := dnsproxy.ReadResolveFile(nsFile); err != nil && rf.Equals(oldRf) {
				continue
			}
			dlog.Infof(c, "Regenerating %s", nsFile)
		} else {
			s.domains[domain] = struct{}{}
			dlog.Infof(c, "Generating %s", nsFile)
		}
		if err := rf.Write(nsFile); err != nil {
			dlog.Error(c, err)
		}
	}
	s.flushDNS()
	return nil
}

func domainResolverFile(resolverDirName, domain string) string {
	return filepath.Join(resolverDirName, "telepresence."+domain)
}
