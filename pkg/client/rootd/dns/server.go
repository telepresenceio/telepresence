package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v3"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type Resolver func(context.Context, *dns.Question) (dnsproxy.RRs, int, error)

const (
	// defaultClusterDomain used unless traffic-manager reports otherwise.
	defaultClusterDomain = "cluster.local."

	// sanityCheck is the query used when verifying that a DNS query reaches our DNS server. It should result
	// in an increase of the requestCount but always yield an NXDOMAIN reply.
	santiyCheck    = "jhfweoitnkgyeta." + tel2SubDomain
	santiyCheckDot = santiyCheck + "."

	// dnsTTL is the number of seconds that a found DNS record should be allowed to live in the callers cache. We
	// keep this low to avoid such caching.
	dnsTTL = 4
)

type FallbackPool interface {
	Exchange(context.Context, *dns.Client, *dns.Msg) (*dns.Msg, time.Duration, error)
	RemoteAddr() netip.Addr
	LocalAddrs() []*net.UDPAddr
	Close()
}

const (
	_ = int32(iota)
	recursionQueryNotYetReceived
	recursionQueryReceived
	recursionNotDetected
	recursionDetected
)

var DefaultExcludeSuffixes = client.DefaultExcludeSuffixes //nolint:gochecknoglobals // constant

type nsAndDomains struct {
	domains   []string
	namespace string
}

// Server is a DNS server which implements the github.com/miekg/dns Handler interface.
type Server struct {
	sync.RWMutex
	client.DNS
	ctx          context.Context // necessary to make logging work in ServeDNS function
	fallbackPool FallbackPool
	resolve      Resolver
	requestCount int64
	cache        *xsync.MapOf[cacheKey, *cacheEntry]
	recursive    int32 // one of the recursionXXX constants declared above (unique type avoided because it just gets messy with the atomic calls)

	// Suffixes to immediately drop from the query before processing. This list will always contain the tel2Search domain.
	// The overriding resolver will also add the search path found in /etc/resolv.conf, because that search path is not
	// intended for this resolver and will get reapplied when passing things on to the fallback resolver.
	dropSuffixes []string

	// routes are typically namespaces, accessible using <service-name>.<namespace-name>.
	routes map[string]struct{}

	// search are appended to a query to form new names that are then dispatched to the
	// DNS resolver. The act of appending is not performed by this server, but rather
	// by the system's DNS resolver before calling on this server.
	search []string

	// domains contains the sum of the include-suffixes and routes. It is currently only
	// used by the darwin resolver to keep track of files to add or remove.
	domains map[string]struct{}

	// nsAndDomainsCh receives requests to change the top level domains and the search path.
	nsAndDomainsCh chan nsAndDomains

	// clusterDomain reported by the traffic-manager
	clusterDomain string

	// Function that sends a lookup request to the traffic-manager
	clusterLookup Resolver

	// mappingsMap is contains the same mappings as DNS.Mappings but as a map (for performance).
	mappingsMap map[string]string

	error string

	// ready is closed when the DNS server is fully configured
	ready chan struct{}
}

type cacheEntry struct {
	created      time.Time
	currentQType int32 // will be set to the current qType during call to cluster
	answer       dnsproxy.RRs
	rCode        int
	wait         chan struct{}
}

// cacheTTL is the time to live for an entry in the local DNS cache.
const cacheTTL = 60 * time.Second

func (dv *cacheEntry) expired() bool {
	return time.Since(dv.created) > cacheTTL
}

func (dv *cacheEntry) close() {
	select {
	case <-dv.wait:
	default:
		close(dv.wait)
	}
}

func sliceToLower(ss []string) []string {
	for i, s := range ss {
		ss[i] = strings.ToLower(s)
	}
	return ss
}

// NewServer returns a new dns.Server.
func NewServer(config *client.DNS, clusterLookup Resolver) *Server {
	if config == nil {
		config = &client.DNS{}
	}
	if len(config.ExcludeSuffixes) == 0 {
		config.ExcludeSuffixes = DefaultExcludeSuffixes
	}
	if config.LookupTimeout <= 0 {
		config.LookupTimeout = 8 * time.Second
	}
	return &Server{
		DNS:            *config,
		mappingsMap:    mappingsMap(config.Mappings),
		cache:          xsync.NewMapOf[cacheKey, *cacheEntry](),
		routes:         make(map[string]struct{}),
		domains:        make(map[string]struct{}),
		dropSuffixes:   []string{tel2SubDomainDot},
		search:         []string{tel2SubDomain},
		nsAndDomainsCh: make(chan nsAndDomains, 5),
		clusterDomain:  defaultClusterDomain,
		clusterLookup:  clusterLookup,
		ready:          make(chan struct{}),
	}
}

// tel2SubDomain helps differentiate between single label and qualified DNS queries.
//
// Dealing with single label names is tricky because what we really want is to receive the
// name and then forward it verbatim to the DNS resolver in the cluster so that it can
// add whatever search paths to it that it sees fit, but in order to receive single name
// queries in the first place, our DNS resolver must have a search path that adds a domain
// that the DNS system knows that we will handle.
//
// Example flow:
// The user queries for the name "alpha". The DNS system on the host tries the search path
// of our DNS resolver which contains "tel2-search" and creates the name "alpha.tel2-search".
// The DNS system now discovers that our DNS resolver handles that domain, so we receive
// the query. We then strip the "tel2-search" and send the original single label name to the
// cluster, and we add it back before we forward the reply.
const (
	tel2SubDomain    = "tel2-search"
	tel2SubDomainDot = tel2SubDomain + "."
)

// wpadDot is used when rejecting all WPAD (Wep Proxy Auto-Discovery) queries.
const wpadDot = "wpad."

var (
	localhostIPv4 = net.IP{127, 0, 0, 1}                                   //nolint:gochecknoglobals // constant
	localhostIPv6 = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1} //nolint:gochecknoglobals // constant
)

func (s *Server) shouldDoClusterLookup(query string) bool {
	name := query[:len(query)-1] // skip last dot
	if strings.HasPrefix(query, wpadDot) {
		// Reject "wpad.*"
		dlog.Debugf(s.ctx, `Cluster DNS excluded by exclude-prefix "wpad." for name %q`, name)
		return false
	}

	if s.isExcluded(name) {
		// Reject any host explicitly added to the exclude list.
		dlog.Debugf(s.ctx, "Cluster DNS explicitly excluded for name %q", name)
		return false
	}

	if !strings.ContainsRune(name, '.') {
		// Single label names are always included.
		dlog.Debugf(s.ctx, "Cluster DNS included for single label name %q", name)
		return true
	}

	// Skip configured exclude-suffixes unless also matched by an include-suffix
	// that is longer (i.e. more specific).
	for _, es := range s.ExcludeSuffixes {
		if strings.HasSuffix(name, es) {
			// Exclude unless more specific include.
			for _, is := range s.IncludeSuffixes {
				if len(is) >= len(es) && strings.HasSuffix(name, is) {
					dlog.Debugf(s.ctx,
						"Cluster DNS included by include-suffix %q (overriding exclude-suffix %q) for name %q", is, es, name)
					return true
				}
			}
			dlog.Debugf(s.ctx, "Cluster DNS excluded by exclude-suffix %q for name %q", es, name)
			return false
		}
	}

	// Always include configured search paths
	for _, sfx := range s.search {
		if strings.HasSuffix(name, sfx) {
			dlog.Debugf(s.ctx, "Cluster DNS included by search %q of name %q", sfx, name)
			return true
		}
	}

	// Always include configured routes
	for sfx := range s.routes {
		if strings.HasSuffix(name, sfx) {
			dlog.Debugf(s.ctx, "Cluster DNS included by namespace %q of name %q", sfx, name)
			return true
		}
	}

	// Always include queries for the cluster domain.
	if strings.HasSuffix(query, "."+s.clusterDomain) {
		dlog.Debugf(s.ctx, "Cluster DNS included by cluster domain %q of name %q", s.clusterDomain, name)
		return true
	}

	// Always include configured includeSuffixes
	for _, sfx := range s.IncludeSuffixes {
		if strings.HasSuffix(name, sfx) {
			dlog.Debugf(s.ctx,
				"Cluster DNS included by include-suffix %q for name %q", sfx, name)
			return true
		}
	}

	// Pass any queries for the cluster domain.
	dlog.Debugf(s.ctx, "Cluster DNS excluded for name %q. No inclusion rule was matched", name)
	return false
}

func (s *Server) isExcluded(name string) bool {
	if slice.Contains(s.Excludes, name) {
		return true
	}

	// When intercepting, this function will potentially receive the hostname of any search param, so their
	// unqualified hostname should be evaluated too.
	qLen := len(name)
	for _, sp := range s.search {
		if strings.HasSuffix(name, "."+sp) && slice.Contains(s.Excludes, name[:qLen-len(sp)-1]) {
			return true
		}
	}
	return false
}

func (s *Server) isDomainExcluded(name string) bool {
	return slices.Contains(s.ExcludeSuffixes, "."+name)
}

func (s *Server) resolveInCluster(c context.Context, q *dns.Question) (result dnsproxy.RRs, rCode int, err error) {
	query := q.Name
	if query == "localhost." {
		// BUG(lukeshu): I have no idea why a lookup
		// for localhost even makes it to here on my
		// home WiFi when connecting to a k3sctl
		// cluster (but not a kubernaut.io cluster).
		// But it does, so I need this in order to be
		// productive at home.  We should really
		// root-cause this, because it's weird.
		hdr := dns.RR_Header{
			Name:   q.Name,
			Rrtype: q.Qtype,
			Class:  q.Qclass,
		}
		switch q.Qtype {
		case dns.TypeA:
			return dnsproxy.RRs{&dns.A{
				Hdr: hdr,
				A:   localhostIPv4,
			}}, dns.RcodeSuccess, nil
		case dns.TypeAAAA:
			return dnsproxy.RRs{&dns.AAAA{
				Hdr:  hdr,
				AAAA: localhostIPv6,
			}}, dns.RcodeSuccess, nil
		default:
			return nil, dns.RcodeNameError, nil
		}
	}

	if !s.shouldDoClusterLookup(query) {
		return nil, dns.RcodeNameError, nil
	}

	// Give the cluster lookup a reasonable timeout.
	c, cancel := context.WithTimeout(c, s.LookupTimeout)
	defer cancel()

	result, rCode, err = s.clusterLookup(c, q)
	if err != nil {
		return nil, rCode, client.CheckTimeout(c, err)
	}

	// Keep the TTLs of requests resolved in the cluster low. We
	// cache them locally anyway, but our cache is flushed when things are
	// intercepted or the namespaces change.
	for _, rr := range result {
		if h := rr.Header(); h != nil {
			h.Ttl = dnsTTL
		}
	}
	return result, rCode, nil
}

func (s *Server) GetConfig() *client.DNS {
	var d client.DNS
	s.RLock()
	d = s.DNS
	s.RUnlock()
	return &d
}

func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

func (s *Server) Stop() {
	// Close s.ready unless it's already closed
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
}

func (s *Server) SetClusterDNS(dns *manager.DNS, remoteIP netip.Addr) {
	s.Lock()
	if !s.RemoteIP.IsValid() {
		s.RemoteIP = remoteIP
	}
	if dns != nil {
		if slices.Equal(s.ExcludeSuffixes, DefaultExcludeSuffixes) && len(dns.ExcludeSuffixes) > 0 {
			s.ExcludeSuffixes = sliceToLower(dns.ExcludeSuffixes)
		}
		if len(s.IncludeSuffixes) == 0 {
			s.IncludeSuffixes = sliceToLower(dns.IncludeSuffixes)
		}
		s.clusterDomain = strings.ToLower(dns.ClusterDomain)
	}
	s.Unlock()
}

// SetTopLevelDomainsAndSearchPath updates the DNS top level domains and the search path used by the resolver.
func (s *Server) SetTopLevelDomainsAndSearchPath(ctx context.Context, domains []string, namespace string) {
	das := nsAndDomains{
		domains:   domains,
		namespace: namespace,
	}
	select {
	case <-ctx.Done():
	case s.nsAndDomainsCh <- das:
	}
}

func (s *Server) purgeRecordsFromCache(keyName string) {
	keyName = strings.TrimSuffix(keyName, ".") + "."
	for _, qType := range []uint16{dns.TypeA, dns.TypeAAAA} {
		toDeleteKey := cacheKey{name: keyName, qType: qType}
		if old, ok := s.cache.LoadAndDelete(toDeleteKey); ok {
			old.close()
		}
	}
}

// SetExcludes sets the excludes list in the config.
func (s *Server) SetExcludes(excludes []string) {
	for i, e := range excludes {
		excludes[i] = strings.ToLower(e)
	}
	s.Lock()
	oldExcludes := s.Excludes
	s.Excludes = excludes
	s.Unlock()

	for _, e := range slice.AppendUnique(oldExcludes, excludes...) {
		s.purgeRecordsFromCache(e)
	}
}

func mappingsMap(mappings []*client.DNSMapping) map[string]string {
	if l := len(mappings); l > 0 {
		mm := make(map[string]string, l)
		for _, m := range mappings {
			al := m.AliasFor
			if ip := iputil.Parse(al); ip == nil {
				al += "."
			}
			mm[strings.ToLower(m.Name+".")] = strings.ToLower(al)
		}
		return mm
	}
	return nil
}

// SetMappings sets the Mappings list in the config.
func (s *Server) SetMappings(mappings []*rpc.DNSMapping) {
	ml := client.MappingsFromRPC(mappings)
	mm := mappingsMap(ml)
	s.Lock()
	s.Mappings = ml
	s.mappingsMap = mm
	s.Unlock()

	// Flush the mappings.
	for n := range mm {
		s.purgeRecordsFromCache(n)
	}
}

func newLocalUDPListener(c context.Context) (net.PacketConn, error) {
	lc := &net.ListenConfig{}
	return lc.ListenPacket(c, "udp", "127.0.0.1:0")
}

func (s *Server) processSearchPaths(g *dgroup.Group, processor func(context.Context, vif.Device) error, dev vif.Device) {
	g.Go("SearchPaths", func(c context.Context) error {
		s.performRecursionCheck(c)
		prevDas := nsAndDomains{
			domains:   []string{},
			namespace: "",
		}
		unchanged := func(das nsAndDomains) bool {
			return das.namespace == prevDas.namespace && slices.Equal(das.domains, prevDas.domains)
		}

		for {
			select {
			case <-c.Done():
				return nil
			case das := <-s.nsAndDomainsCh:
				// Only interested in the last one, and only if it differs
				if len(s.nsAndDomainsCh) > 0 || unchanged(das) {
					continue
				}
				prevDas = das

				routes := make(map[string]struct{}, len(das.domains))
				for _, domain := range das.domains {
					if domain != "" && !s.isDomainExcluded(domain) {
						routes[domain] = struct{}{}
					}
				}
				if !s.isDomainExcluded("svc") {
					routes["svc"] = struct{}{}
				}
				s.Lock()
				s.routes = routes

				// The connected namespace must be included as a search path for the cases
				// where it's up to the traffic-manager to resolve. It cannot resolve a single
				// label name intended for other namespaces.
				s.search = []string{tel2SubDomain, das.namespace}
				s.Unlock()

				if err := processor(c, dev); err != nil {
					return err
				}
			}
		}
	})
}

func (s *Server) flushDNS() {
	s.cache.Range(func(key cacheKey, _ *cacheEntry) bool {
		if old, ok := s.cache.LoadAndDelete(key); ok {
			old.close()
		}
		return true
	})
}

// splitToUDPAddr splits the given address into an UDPAddr. It's
// an  error if the address is based on a hostname rather than an IP.
func splitToUDPAddr(netAddr net.Addr) (*net.UDPAddr, error) {
	ip, port, err := iputil.SplitToIPPort(netAddr)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

// RequestCount returns the number of requests that this server has received.
func (s *Server) RequestCount() int {
	return int(atomic.LoadInt64(&s.requestCount))
}

func copyRRs(rrs dnsproxy.RRs, qTypes []uint16) dnsproxy.RRs {
	if len(rrs) == 0 {
		return rrs
	}
	cp := make(dnsproxy.RRs, 0, len(rrs))
	for _, rr := range rrs {
		if slice.Contains(qTypes, rr.Header().Rrtype) {
			cp = append(cp, dns.Copy(rr))
		}
	}
	return cp
}

type cacheKey struct {
	name  string
	qType uint16
}

func (c *cacheKey) String() string {
	return fmt.Sprintf("%s %s", dns.TypeToString[c.qType], c.name)
}

const (
	// recursionCheck is a special host name in a well known namespace that isn't expected to exist. It
	// is used once for determining if the cluster's DNS resolver will call the Telepresence DNS resolver
	// recursively. This is common when the cluster is running on the local host (k3s in docker for instance).
	//
	// The check is performed using the following steps.
	// 1. A lookup is made for "tel-recursion-check
	// 2. When our DNS-resolver receives this lookup, it modifies the name to "tel2-recursion-check.kube-system."
	//    and sends that on to the cluster.
	// 3. If our DNS-resolver now encounters a query for the "tel2-recursion-check.kube-system.", then we know
	//    that a recursion took place.
	// 4. If no request for "tel2-recursion-check.kube-system." is received, then it's assumed that the resolver
	//    is not recursive.
	recursionCheck  = "tel2-recursion-check."
	recursionCheck2 = "tel2-recursion-check.kube-system."
)

func (s *Server) resolveWithRecursionCheck(q *dns.Question) (dnsproxy.RRs, int, error) {
	if strings.HasPrefix(q.Name, recursionCheck) {
		if strings.HasPrefix(q.Name, recursionCheck2) {
			if atomic.CompareAndSwapInt32(&s.recursive, recursionQueryReceived, recursionDetected) {
				dlog.Debug(s.ctx, "DNS resolver is recursive")
			}
			return nil, dns.RcodeNameError, nil
		}

		if atomic.CompareAndSwapInt32(&s.recursive, recursionQueryNotYetReceived, recursionQueryReceived) {
			tc, cancel := context.WithTimeout(s.ctx, recursionTestTimeout)
			go func() {
				defer cancel()
				nq := *q // by value copy
				nq.Name = recursionCheck2
				_, _, _ = s.resolveInCluster(s.ctx, &nq) // We really don't care about the reply here.
			}()
			<-tc.Done()

			// When we've gotten the reply from the cluster, we know if recursion did occur.
			if atomic.CompareAndSwapInt32(&s.recursive, recursionQueryReceived, recursionNotDetected) {
				dlog.Debug(s.ctx, "DNS resolver is not recursive")
			}
		}
		return localHostReply(q), dns.RcodeSuccess, nil
	}

	answer, rCode, err := s.resolveThruCache(q)
	if err != nil || rCode != dns.RcodeSuccess {
		// For A and AAAA queries, we check if we have a successful counterpart in the cache. If we
		// do, then this query must return NOERROR EMPTY
		ck := cacheKey{name: q.Name, qType: dns.TypeNone}
		switch q.Qtype {
		case dns.TypeA:
			ck.qType = dns.TypeAAAA
		case dns.TypeAAAA:
			ck.qType = dns.TypeA
		}
		if ck.qType != dns.TypeNone {
			if ce, ok := s.cache.Load(ck); ok {
				<-ce.wait
				if !ce.expired() && ce.rCode == dns.RcodeSuccess && atomic.LoadInt32(&ce.currentQType) == int32(ck.qType) {
					dlog.Debugf(s.ctx, "found counterpart for %s %s", dns.TypeToString[uint16(ce.currentQType)], ce.answer)
					err = nil
					rCode = dns.RcodeSuccess
				}
			}
		}
	}
	return answer, rCode, err
}

// resolveThruCache resolves the given query by first performing a cache lookup. If a cached
// entry is found that hasn't expired, it's returned. If not, this function will call
// resolveQuery() to resolve and store in the case.
func (s *Server) resolveThruCache(q *dns.Question) (answer dnsproxy.RRs, rCode int, err error) {
	dv := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	key := cacheKey{name: q.Name, qType: q.Qtype}
	if oldDv, loaded := s.cache.LoadOrStore(key, dv); loaded {
		if atomic.LoadInt32(&s.recursive) == recursionDetected && atomic.LoadInt32(&oldDv.currentQType) == int32(q.Qtype) {
			// We have to assume that this is a recursion from the cluster.
			dlog.Debugf(s.ctx, "returning error for query %q: assumed to be recursive", key.String())
			return nil, dns.RcodeNameError, nil
		}
		<-oldDv.wait
		if !oldDv.expired() {
			qTypes := []uint16{q.Qtype}
			if q.Qtype != dns.TypeCNAME {
				// Allow additional CNAME records if they are present.
				for _, rr := range oldDv.answer {
					if rr.Header().Rrtype == dns.TypeCNAME {
						qTypes = append(qTypes, dns.TypeCNAME)
						break
					}
				}
			}
			return copyRRs(oldDv.answer, qTypes), oldDv.rCode, nil
		}
		s.cache.Store(key, dv)
	}

	atomic.StoreInt32(&dv.currentQType, int32(q.Qtype))
	defer func() {
		if rCode != dns.RcodeSuccess {
			s.cache.Delete(key) // Don't cache unless the lookup succeeded.
		} else {
			dv.answer = answer
			dv.rCode = rCode

			// Return a result for the correct query type. The result will be nil (nxdomain) if nothing was found. It might
			// also be empty if no RRs were found for the given query type and that is OK.
			// See https://datatracker.ietf.org/doc/html/rfc4074#section-3
			answer = copyRRs(answer, []uint16{q.Qtype})
		}
		atomic.StoreInt32(&dv.currentQType, int32(dns.TypeNone))
		dv.close()
	}()
	return s.resolve(s.ctx, q)
}

// dfs is a func that implements the fmt.Stringer interface. Used in log statements to ensure
// that the function isn't evaluated until the log output is formatted (which will happen only
// if the given loglevel is enabled).
type dfs func() string

func (d dfs) String() string {
	return d()
}

func (s *Server) performRecursionCheck(c context.Context) {
	s.Lock()
	if _, ok := s.routes["kube-system"]; !ok {
		s.routes["kube-system"] = struct{}{}
		nl := len(s.routes)
		defer func() {
			s.Lock()
			if nl == len(s.routes) {
				delete(s.routes, "kube-system")
			}
			s.Unlock()
		}()
	}
	s.Unlock()
	defer func() {
		dlog.Debug(c, "Recursion check finished")
		close(s.ready)
	}()
	rc := recursionCheck + tel2SubDomain
	dlog.Debugf(c, "Performing initial recursion check with %s", rc)
	i := 0
	atomic.StoreInt32(&s.recursive, recursionQueryNotYetReceived)
	for ; c.Err() == nil && i < maxRecursionTestRetries && atomic.LoadInt32(&s.recursive) == recursionQueryNotYetReceived; i++ {
		go func() {
			_, _ = net.DefaultResolver.LookupIP(c, "ip4", rc)
		}()
		time.Sleep(500 * time.Millisecond)
	}
	if i == maxRecursionTestRetries {
		msg := "DNS doesn't seem to work properly"
		dlog.Error(c, msg)
		s.Lock()
		s.error = msg
		s.Unlock()
		return
	}
	// Await result
	for c.Err() == nil {
		rc := atomic.LoadInt32(&s.recursive)
		if rc == recursionDetected || rc == recursionNotDetected {
			break
		}
		dtime.SleepWithContext(c, 10*time.Millisecond)
	}
}

func localHostReply(q *dns.Question) dnsproxy.RRs {
	switch q.Qtype {
	case dns.TypeA:
		return dnsproxy.RRs{&dns.A{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: q.Qtype,
				Class:  q.Qclass,
			},
			A: localhostIPv4,
		}}
	case dns.TypeAAAA:
		return dnsproxy.RRs{&dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: q.Qtype,
				Class:  q.Qclass,
			},
			AAAA: localhostIPv6,
		}}
	default:
		return nil
	}
}

// ServeDNS is an implementation of github.com/miekg/dns Handler.ServeDNS.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	c := s.ctx
	atomic.AddInt64(&s.requestCount, 1)

	q := &r.Question[0]
	qts := dns.TypeToString[q.Qtype]
	dlog.Debugf(c, "ServeDNS %5d %-6s %s", r.Id, qts, q.Name)

	msg := new(dns.Msg)
	var pfx dfs = func() string { return "" }
	var txt dfs = func() string { return "" }
	var rct dfs = func() string { return dns.RcodeToString[msg.Rcode] }

	defer func() {
		dlog.Debugf(c, "%s%5d %-6s %s -> %s %s", pfx, r.Id, qts, q.Name, rct, txt)
		_ = w.WriteMsg(msg)

		// Closing the response tells the DNS service to terminate
		if c.Err() != nil {
			_ = w.Close()
		}
	}()

	// The sanity-check query is sent during the configuration phase of the DNS server and then
	// never again. It must reply with localhost.
	//
	// NOTE! The sanity-check will always use the tel2-search subdomain, so the check made here
	//       must be made before the tel2-search is removed.
	if q.Name == santiyCheckDot {
		answer := localHostReply(q)
		if answer == nil {
			msg.SetRcode(r, dns.RcodeNotImplemented)
			return
		}
		msg.SetReply(r)
		msg.Answer = answer
		msg.Authoritative = true
		txt = func() string { return answer.String() }
		dlog.Debug(c, "sanity-check OK")
		return
	}

	if !dnsproxy.SupportedType(q.Qtype) {
		msg.SetRcode(r, dns.RcodeNotImplemented)
		return
	}

	// We make changes to the query name, so we better restore it prior to writing an
	// answer back, or the caller will get confused.
	origName := q.Name
	defer func() {
		qs := msg.Question
		if len(qs) > 0 {
			mq := &qs[0] // Important to use a pointer here. We don't want to change a by-value copied struct.
			if mq.Name == q.Name {
				mq.Name = origName
			}
		}
		for _, rr := range msg.Answer {
			h := rr.Header()
			if h.Name == q.Name {
				h.Name = origName
			}
		}
		q.Name = origName
	}()

	// We're all about lowercase in here
	q.Name = strings.ToLower(origName)

	// The tel2SubDomain serves one purpose and one purpose alone. It's there to coerce the
	// system DNS resolver to direct requests to this resolver. The system configuration to
	// make this happen vary depending on OS, but the purpose is always the same. Given that,
	// the first step in the resolution is to remove this domain-suffix if it exists.
	ln := len(q.Name)
	for _, dropSuffix := range s.dropSuffixes {
		if strings.HasSuffix(q.Name, dropSuffix) {
			// Remove the suffix and ensure that the name still ends
			// with a dot after the removal. If it doesn't, then this
			// was not really a domain suffix but rather a partial
			// domain name.
			n := q.Name[:ln-len(dropSuffix)]
			if last := len(n) - 1; last > 0 && n[last] == '.' {
				q.Name = n
				break
			}
		}
	}

	var answer dnsproxy.RRs
	var rCode int
	var err error

	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME:
		if strings.Contains(q.Name, tel2SubDomainDot) {
			// This is a bogus name because it has some domain after
			// the tel2-search domain. Should normally never happen, but
			// will happen if someone queries for the tel2-search domain
			// as a single label name.
			msg.SetRcode(r, dns.RcodeNameError)
			return
		}

		// try and resolve any mappings before consulting the cache, so that mapping hits don't
		// end up in the cache.
		answer, rCode, err = s.resolveMapping(q)
		if err == errNoMapping {
			answer, rCode, err = s.resolveWithRecursionCheck(q)
		}
	case dns.TypePTR:
		// Respond with cluster domain if the queried IP is the IP of this DNS server.
		if ip, err := dnsproxy.PtrAddress(q.Name); err == nil && ip == s.RemoteIP {
			answer = dnsproxy.RRs{
				&dns.PTR{
					Hdr: dnsproxy.NewHeader(q.Name, q.Qtype),
					Ptr: s.clusterDomain,
				},
			}
			rCode = dns.RcodeSuccess
			break
		}
		fallthrough
	default:
		answer, rCode, err = s.resolveWithRecursionCheck(q)
	}

	if err == nil && rCode == dns.RcodeSuccess {
		msg.SetReply(r)
		msg.Answer = answer
		msg.Authoritative = true
		msg.RecursionAvailable = s.fallbackPool != nil
		txt = func() string { return answer.String() }
		return
	}

	// The recursion check query, or queries that end with the cluster domain name, are not dispatched to the
	// fallback DNS-server.
	s.RLock()
	cd := s.clusterDomain
	s.RUnlock()
	if s.fallbackPool == nil ||
		strings.HasPrefix(q.Name, recursionCheck2) ||
		strings.HasSuffix(q.Name, cd) ||
		strings.HasSuffix(origName, tel2SubDomainDot) {
		if err != nil {
			rCode = dns.RcodeServerFailure
			if errors.Is(err, context.DeadlineExceeded) {
				txt = func() string { return "timeout" }
			} else {
				txt = err.Error
			}
		}
		msg.SetRcode(r, rCode)
	} else {
		// Use the original query name when sending things to the fallback resolver.
		q.Name = origName
		pfx = func() string { return fmt.Sprintf("(%s) ", s.fallbackPool.RemoteAddr()) }
		msg, txt = s.fallbackExchange(c, msg, r)
	}
}

func (s *Server) fallbackExchange(c context.Context, msg, r *dns.Msg) (*dns.Msg, func() string) {
	dc := &dns.Client{Net: "udp", Timeout: s.LookupTimeout}
	poolMsg, _, err := s.fallbackPool.Exchange(c, dc, r)
	var txt func() string
	if err != nil {
		rCode := dns.RcodeServerFailure
		txt = err.Error
		if netErr, ok := err.(net.Error); ok {
			switch {
			case netErr.Timeout():
				txt = func() string { return "timeout" }
			case netErr.Temporary(): //nolint:staticcheck // err.Temporary is deprecated
				rCode = dns.RcodeRefused
			default:
			}
		}
		msg.SetRcode(r, rCode)
	} else {
		msg = poolMsg
		msg.RecursionAvailable = true
		txt = func() string { return dnsproxy.RRs(msg.Answer).String() }
	}
	return msg, txt
}

var errNoMapping = errors.New("no mapping") //nolint:gochecknoglobals // constant

func (s *Server) resolveMapping(q *dns.Question) (dnsproxy.RRs, int, error) {
	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA, dns.TypeCNAME:
	default:
		return nil, dns.RcodeNameError, errNoMapping
	}

	s.RLock()
	mappingAlias, ok := s.mappingsMap[q.Name]
	s.RUnlock()

	if !ok {
		return nil, dns.RcodeNameError, errNoMapping
	}
	if ip := iputil.Parse(mappingAlias); ip != nil {
		// The name resolves to an A or AAAA record known by this DNS server.
		var rrs dnsproxy.RRs
		if q.Qtype == dns.TypeA && len(ip) == 4 {
			rrs = dnsproxy.RRs{&dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
				A:   ip,
			}}
		} else if q.Qtype == dns.TypeAAAA && len(ip) == 16 {
			rrs = dnsproxy.RRs{&dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: dnsTTL},
				AAAA: ip,
			}}
		}
		return rrs, dns.RcodeSuccess, nil
	}

	cnameRRs := dnsproxy.RRs{&dns.CNAME{
		Hdr:    dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: dnsTTL},
		Target: mappingAlias,
	}}

	if q.Qtype == dns.TypeCNAME {
		// A query for the CNAME must only return the CNAME.
		return cnameRRs, dns.RcodeSuccess, nil
	}

	// A query for an A or AAAA must resolve the CNAME and then return both the result and the
	// CNAME that resolved to it.
	answer, rCode, err := s.resolveWithRecursionCheck(&dns.Question{
		Name:   mappingAlias,
		Qtype:  q.Qtype,
		Qclass: q.Qclass,
	})
	if err == nil {
		answer = append(cnameRRs, answer...)
	}
	return answer, rCode, err
}

// Run starts the DNS server(s) and waits for them to end.
func (s *Server) Run(c context.Context, initDone chan<- struct{}, listeners []net.PacketConn, fallbackPool FallbackPool, resolve Resolver) error {
	s.ctx = c
	s.fallbackPool = fallbackPool
	s.resolve = resolve

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	for _, listener := range listeners {
		srv := &dns.Server{PacketConn: listener, Handler: s, ReadTimeout: time.Second}
		g.Go(listener.LocalAddr().String(), func(c context.Context) error {
			go func() {
				<-c.Done()
				dlog.Debugf(c, "Shutting down DNS server")
				_ = srv.ShutdownContext(dcontext.HardContext(c))
			}()
			return srv.ActivateAndServe()
		})
	}
	close(initDone)
	return g.Wait()
}
