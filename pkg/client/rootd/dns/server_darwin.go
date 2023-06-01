package dns

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 10
	recursionTestTimeout    = 500 * time.Millisecond
)

func (r *resolveFile) setSearchPaths(paths ...string) {
	ps := make([]string, 0, len(paths)+1)
	for _, p := range paths {
		p = strings.TrimSuffix(p, ".")
		if len(p) > 0 && p != r.domain {
			ps = append(ps, p)
		}
	}
	r.search = ps
}

func (r *resolveFile) write(fileName string) error {
	var buf bytes.Buffer
	_, _ = r.WriteTo(&buf)
	return os.WriteFile(fileName, buf.Bytes(), 0o644)
}

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
	resolverFileName := filepath.Join(resolverDirName, "telepresence.local")

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

	kubernetesZone := s.clusterDomain
	if kubernetesZone == "" {
		kubernetesZone = "cluster.local."
	}

	kubernetesZone = kubernetesZone[:len(kubernetesZone)-1] // strip trailing dot
	rf := resolveFile{
		port:        dnsAddr.Port,
		domain:      kubernetesZone,
		nameservers: []string{dnsAddr.IP.String()},
		search:      []string{tel2SubDomainDot + kubernetesZone},
	}

	if err = rf.write(resolverFileName); err != nil {
		return err
	}
	dlog.Infof(c, "Generated new %s", resolverFileName)

	defer func() {
		// Remove the main resolver file
		_ = os.Remove(resolverFileName)

		// Remove each namespace resolver file
		for domain := range s.domains {
			_ = os.Remove(domainResolverFile(resolverDirName, domain))
		}
		s.flushDNS()
	}()

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		s.processSearchPaths(g, func(c context.Context, paths []string, _ vif.Device) error {
			return s.updateResolverFiles(c, resolverDirName, resolverFileName, dnsAddr, paths)
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

func (s *Server) updateResolverFiles(c context.Context, resolverDirName, resolverFileName string, dnsAddr *net.UDPAddr, paths []string) error {
	dlog.Infof(c, "setting search paths %s", strings.Join(paths, " "))
	rf, err := readResolveFile(resolverFileName)
	if err != nil {
		return err
	}

	// paths that contain a dot are search paths, the ones that don't are namespaces.
	namespaces := make(map[string]struct{})
	search := make([]string, 0)
	for _, path := range paths {
		if strings.ContainsRune(path, '.') {
			search = append(search, path)
		} else if path != "" {
			namespaces[path] = struct{}{}
		}
	}

	// All namespaces and include suffixes become domains
	domains := make(map[string]struct{}, len(namespaces)+len(s.config.IncludeSuffixes))
	maps.Merge(domains, namespaces)
	for _, sfx := range s.config.IncludeSuffixes {
		domains[strings.TrimPrefix(sfx, ".")] = struct{}{}
	}

	s.domainsLock.Lock()
	defer s.domainsLock.Unlock()

	// On Darwin, we provide resolution of NAME.NAMESPACE by adding one domain
	// for each namespace in its own domain file under /etc/resolver. Each file
	// is named "telepresence.<domain>.local"
	var removals []string
	var additions []string

	for domain := range s.domains {
		if _, ok := domains[domain]; !ok {
			removals = append(removals, domain)
		}
	}
	for domain := range domains {
		if _, ok := s.domains[domain]; !ok {
			additions = append(additions, domain)
		}
	}

	search = append([]string{tel2SubDomainDot + s.clusterDomain}, search...)

	s.search = search
	s.namespaces = namespaces
	s.domains = domains

	for _, domain := range removals {
		nsFile := domainResolverFile(resolverDirName, domain)
		dlog.Infof(c, "Removing %s", nsFile)
		if err = os.Remove(nsFile); err != nil {
			dlog.Error(c, err)
		}
	}
	for _, domain := range additions {
		df := resolveFile{
			port:        dnsAddr.Port,
			domain:      domain,
			nameservers: []string{dnsAddr.IP.String()},
		}
		nsFile := domainResolverFile(resolverDirName, domain)
		dlog.Infof(c, "Generated new %s", nsFile)
		if err = df.write(nsFile); err != nil {
			dlog.Error(c, err)
		}
	}

	rf.setSearchPaths(search...)

	// Versions prior to Big Sur will not trigger an update unless the resolver file
	// is removed and recreated.
	_ = os.Remove(resolverFileName)
	if err = rf.write(resolverFileName); err != nil {
		return err
	}
	s.flushDNS()
	return nil
}

func domainResolverFile(resolverDirName, domain string) string {
	return filepath.Join(resolverDirName, "telepresence."+domain+".local")
}
