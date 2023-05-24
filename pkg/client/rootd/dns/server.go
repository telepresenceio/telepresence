package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type Resolver func(context.Context, *dns.Question) (dnsproxy.RRs, int, error)

// recursionCheck is a special host name in a well known namespace that isn't expected to exist. It
// is used once for determining if the cluster's DNS resolver will call the Telepresence DNS resolver
// recursively. This is common when the cluster is running on the local host (k3s in docker for instance).
const recursionCheck = "tel2-recursion-check.kube-system."

// defaultClusterDomain used unless traffic-manager reports otherwise.
const defaultClusterDomain = "cluster.local."

type FallbackPool interface {
	Exchange(context.Context, *dns.Client, *dns.Msg) (*dns.Msg, time.Duration, error)
	RemoteAddr() string
	LocalAddrs() []*net.UDPAddr
	Close()
}

const (
	_ = int32(iota)
	recursionNotDetected
	recursionDetected
	recursionTestInProgress
)

// Server is a DNS server which implements the github.com/miekg/dns Handler interface.
type Server struct {
	ctx          context.Context // necessary to make logging work in ServeDNS function
	fallbackPool FallbackPool
	resolve      Resolver
	requestCount int64
	cache        sync.Map
	recursive    int32 // one of the recursionXXX constants declared above (unique type avoided because it just gets messy with the atomic calls)
	cacheResolve func(*dns.Question) (dnsproxy.RRs, int, error)
	dropSuffixes []string //nolint:unused // only used on linux

	// Namespaces, accessible using <service-name>.<namespace-name>
	namespaces map[string]struct{}
	domains    map[string]struct{}
	search     []string

	// The domainsLock locks usage of namespaces, domains, and search
	domainsLock sync.RWMutex

	// searchPathCh receives requests to change the search path.
	searchPathCh chan []string

	config *rpc.DNSConfig

	// clusterDomain reported by the traffic-manager
	clusterDomain string

	// Function that sends a lookup request to the traffic-manager
	clusterLookup Resolver

	// onlyNames is set to true when using a legacy traffic-manager incapable of
	// using query types
	onlyNames bool

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

// NewServer returns a new dns.Server.
func NewServer(config *rpc.DNSConfig, clusterLookup Resolver, onlyNames bool) *Server {
	if config == nil {
		config = &rpc.DNSConfig{}
	}
	if len(config.ExcludeSuffixes) == 0 {
		config.ExcludeSuffixes = []string{
			".com",
			".io",
			".net",
			".org",
			".ru",
		}
	}
	if config.LookupTimeout.AsDuration() <= 0 {
		config.LookupTimeout = durationpb.New(8 * time.Second)
	}
	s := &Server{
		config:        config,
		namespaces:    make(map[string]struct{}),
		domains:       make(map[string]struct{}),
		search:        []string{""},
		searchPathCh:  make(chan []string, 5),
		clusterDomain: defaultClusterDomain,
		clusterLookup: clusterLookup,
		onlyNames:     onlyNames,
		ready:         make(chan struct{}),
	}
	s.cacheResolve = s.resolveWithRecursionCheck
	return s
}

// tel2SubDomain fixes a search-path problem when using Docker.
//
// Docker uses its own search-path for single label names. This means that the search path that is
// declared in Telepresence DNS resolver is ignored, although the rest of the DNS-resolution works
// OK. Since the search-path is likely to change during a session, a stable fake domain is needed
// to emulate the search-path. That fake-domain can then be used in the search path declared in the
// Docker config.
//
// The tel2SubDomain fills this purpose and a request for "<single label name>.<tel2SubDomain>"
// will be resolved as "<single label name>.<currently intercepted namespace>".
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
	if strings.HasPrefix(query, wpadDot) {
		// Reject "wpad.*"
		return false
	}
	if strings.HasSuffix(query, "."+s.clusterDomain) && strings.Count(query, ".") < 4 {
		// Reject "<label>.cluster.local."
		return false
	}

	query = query[:len(query)-1] // skip last dot

	if strings.Contains(query, "."+tel2SubDomainDot) {
		// Reject "xxx.tel2-search.xxx" (we know it doesn't end with tel2SubDomain because we just removed the last dot)
		// Addresses like that can come into existence if the daemon runs in a docker container
		// with --dns-search tel2-search and docker in turn uses a DNS server from a VPN that
		// applies search paths to multi-label names.
		// Example when using Tailscape: hello.default.tel2-search.tailbfa9e.ts.net.
		return false
	}

	// Always include configured includeSuffixes
	for _, sfx := range s.config.IncludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return true
		}
	}

	// Skip configured excludeSuffixes
	for _, sfx := range s.config.ExcludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return false
		}
	}
	return true
}

func (s *Server) resolveInCluster(c context.Context, q *dns.Question) (result dnsproxy.RRs, rCode int, err error) {
	origQuery := q.Name
	query := strings.ToLower(origQuery)
	query = strings.TrimSuffix(query, tel2SubDomainDot)
	q.Name = query

	if query == "localhost." {
		// BUG(lukeshu): I have no idea why a lookup
		// for localhost even makes it to here on my
		// home WiFi when connecting to a k3sctl
		// cluster (but not a kubernaut.io cluster).
		// But it does, so I need this in order to be
		// productive at home.  We should really
		// root-cause this, because it's weird.
		switch q.Qtype {
		case dns.TypeA:
			return dnsproxy.RRs{&dns.A{
				Hdr: dns.RR_Header{},
				A:   localhostIPv4,
			}}, dns.RcodeSuccess, nil
		case dns.TypeAAAA:
			return dnsproxy.RRs{&dns.AAAA{
				Hdr:  dns.RR_Header{},
				AAAA: localhostIPv6,
			}}, dns.RcodeSuccess, nil
		}
	}

	if !s.shouldDoClusterLookup(query) {
		return nil, dns.RcodeNameError, nil
	}

	// Give the cluster lookup a reasonable timeout.
	c, cancel := context.WithTimeout(c, s.config.LookupTimeout.AsDuration())
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
			if h.Name == query {
				h.Name = origQuery
			}
			h.Ttl = dnsTTL
		}
	}
	return result, rCode, nil
}

func (s *Server) GetConfig() *rpc.DNSConfig {
	sc := s.config
	return &rpc.DNSConfig{
		LocalIp:         sc.LocalIp,
		RemoteIp:        sc.RemoteIp,
		ExcludeSuffixes: sc.ExcludeSuffixes,
		IncludeSuffixes: sc.IncludeSuffixes,
		LookupTimeout:   sc.LookupTimeout,
		Error:           sc.Error,
	}
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

func (s *Server) SetClusterDNS(dns *manager.DNS, remoteIP net.IP) {
	s.clusterDomain = dns.ClusterDomain
	if s.config == nil {
		s.config = &rpc.DNSConfig{}
	}
	if s.config.RemoteIp == nil {
		s.config.RemoteIp = remoteIP
	}
	contains := func(s []string, a string) bool {
		for _, x := range s {
			if x == a {
				return true
			}
		}
		return false
	}
	appendUnique := func(a, b []string) []string {
		for _, x := range b {
			if !contains(a, x) {
				a = append(a, x)
			}
		}
		return a
	}
	s.config.ExcludeSuffixes = appendUnique(s.config.ExcludeSuffixes, dns.ExcludeSuffixes)
	s.config.IncludeSuffixes = appendUnique(s.config.IncludeSuffixes, dns.IncludeSuffixes)
}

// SetSearchPath updates the DNS search path used by the resolver.
func (s *Server) SetSearchPath(ctx context.Context, paths, namespaces []string) {
	if len(namespaces) > 0 {
		// Provide direct access to intercepted namespaces
		for _, ns := range namespaces {
			paths = append(paths, ns+".svc."+s.clusterDomain)
		}
		paths = append(paths, tel2SubDomain)
	}

	select {
	case <-ctx.Done():
	case s.searchPathCh <- paths:
	}
}

func newLocalUDPListener(c context.Context) (net.PacketConn, error) {
	lc := &net.ListenConfig{}
	return lc.ListenPacket(c, "udp", "127.0.0.1:0")
}

func (s *Server) processSearchPaths(g *dgroup.Group, processor func(context.Context, []string, vif.Device) error, dev vif.Device) {
	g.Go("RecursionCheck", func(c context.Context) error {
		_ = dev.SetDNS(c, s.clusterDomain, s.config.RemoteIp, []string{tel2SubDomain})
		if runtime.GOOS == "windows" {
			// Give the DNS setting some time to take effect.
			dtime.SleepWithContext(c, 500*time.Millisecond)
		}
		s.performRecursionCheck(c)
		return nil
	})

	g.Go("SearchPaths", func(c context.Context) error {
		var prevPaths []string
		unchanged := func(paths []string) bool {
			if len(paths) != len(prevPaths) {
				return false
			}
			for i, path := range paths {
				if path != prevPaths[i] {
					return false
				}
			}
			return true
		}

		for {
			select {
			case <-c.Done():
				return nil
			case paths := <-s.searchPathCh:
				if len(s.searchPathCh) > 0 {
					// Only interested in the last one
					continue
				}
				if !unchanged(paths) {
					dlog.Debugf(c, "%v -> %v", prevPaths, paths)
					prevPaths = make([]string, len(paths))
					copy(prevPaths, paths)
					if err := processor(c, paths, dev); err != nil {
						return err
					}
				}
			}
		}
	})
}

func (s *Server) flushDNS() {
	s.cache.Range(func(key, _ any) bool {
		s.cache.Delete(key)
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

func copyRRs(rrs dnsproxy.RRs, qType uint16) dnsproxy.RRs {
	if len(rrs) == 0 {
		return rrs
	}
	cp := make(dnsproxy.RRs, 0, len(rrs))
	for _, rr := range rrs {
		if rr.Header().Rrtype == qType {
			cp = append(cp, dns.Copy(rr))
		}
	}
	return cp
}

type cacheKey struct {
	name  string
	qType uint16
}

// resolveThruCache resolves the given query by first performing a cache lookup. If a cached
// entry is found that hasn't expired, it's returned. If not, this function will call
// resolveQuery() to resolve and store in the case.
func (s *Server) resolveThruCache(q *dns.Question) (dnsproxy.RRs, int, error) {
	newDv := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	key := cacheKey{name: q.Name, qType: q.Qtype}
	if v, loaded := s.cache.LoadOrStore(key, newDv); loaded {
		oldDv := v.(*cacheEntry)
		if atomic.LoadInt32(&s.recursive) == recursionDetected && atomic.LoadInt32(&oldDv.currentQType) == int32(q.Qtype) {
			// We have to assume that this is a recursion from the cluster.
			return nil, dns.RcodeNameError, nil
		}
		<-oldDv.wait
		if !oldDv.expired() {
			return copyRRs(oldDv.answer, q.Qtype), oldDv.rCode, nil
		}
		s.cache.Store(key, newDv)
	}
	return s.resolveQuery(q, newDv)
}

// resolveWithRecursionCheck is a special version of resolveThruCache which is only used until the
// recursionCheck query has completed, and it has been determined whether a query that is propagated
// to the cluster will recurse back to this resolver or not.
func (s *Server) resolveWithRecursionCheck(q *dns.Question) (dnsproxy.RRs, int, error) {
	newDv := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	key := cacheKey{name: q.Name, qType: q.Qtype}
	if v, loaded := s.cache.LoadOrStore(key, newDv); loaded {
		oldDv := v.(*cacheEntry)
		if strings.HasPrefix(q.Name, recursionCheck) {
			atomic.StoreInt32(&s.recursive, recursionDetected)
		}
		if atomic.LoadInt32(&s.recursive) == recursionDetected {
			return nil, dns.RcodeNameError, nil
		}
		<-oldDv.wait
		if !oldDv.expired() {
			return copyRRs(oldDv.answer, q.Qtype), oldDv.rCode, nil
		}
		s.cache.Store(key, newDv)
	}

	answer, rCode, err := s.resolveQuery(q, newDv)
	if strings.HasPrefix(q.Name, recursionCheck) {
		if atomic.LoadInt32(&s.recursive) == recursionDetected {
			dlog.Debug(s.ctx, "DNS resolver is recursive")
		} else {
			atomic.StoreInt32(&s.recursive, recursionNotDetected)
			dlog.Debug(s.ctx, "DNS resolver is not recursive")
		}
		s.cacheResolve = s.resolveThruCache
	}
	return answer, rCode, err
}

// dfs is a func that implements the fmt.Stringer interface. Used in log statements to ensure
// that the function isn't evaluated until the log output is formatted (which will happen only
// if the given loglevel is enabled).
type dfs func() string

func (d dfs) String() string {
	return d()
}

func (s *Server) performRecursionCheck(c context.Context) {
	defer close(s.ready)
	defer dlog.Debug(c, "Recursion check finished")
	var rc string
	if runtime.GOOS != "darwin" {
		rc = recursionCheck + tel2SubDomain
	} else {
		rc = recursionCheck + s.clusterDomain
	}
	dlog.Debugf(c, "Performing initial recursion check with %s", rc)
	i := 0
	atomic.StoreInt32(&s.recursive, recursionTestInProgress)
	for ; i < maxRecursionTestRetries && atomic.LoadInt32(&s.recursive) == recursionTestInProgress; i++ {
		// Recursion is typically very fast (all on the same host) so let's
		// use short timeouts
		if i > 0 {
			dlog.Debug(c, "retrying recursion check")
		}
		tc, cancel := context.WithTimeout(c, recursionTestTimeout)
		_, err := net.DefaultResolver.LookupIP(tc, "ip4", rc)
		cancel()
		if err != nil {
			if derr, ok := err.(*net.DNSError); ok {
				if atomic.LoadInt32(&s.recursive) != recursionTestInProgress {
					if derr.IsTimeout || derr.IsNotFound {
						return
					}
				}
				if derr.IsTimeout {
					dtime.SleepWithContext(c, 200*time.Millisecond)
					continue
				}
			}
			dlog.Errorf(c, "unexpected error during recursion check: %v", err)
		}
		if atomic.LoadInt32(&s.recursive) != recursionTestInProgress {
			return
		}
		// Check didn't hit our resolver. Try again
		dtime.SleepWithContext(c, 100*time.Millisecond)
	}
	if i == maxRecursionTestRetries {
		s.config.Error = "DNS doesn't seem to work properly"
	}
}

// ServeDNS is an implementation of github.com/miekg/dns Handler.ServeDNS.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	c := s.ctx
	defer func() {
		// Closing the response tells the DNS service to terminate
		if c.Err() != nil {
			_ = w.Close()
		}
	}()

	q := &r.Question[0]
	qts := dns.TypeToString[q.Qtype]
	dlog.Debugf(c, "ServeDNS %5d %-6s %s", r.Id, qts, q.Name)

	atomic.AddInt64(&s.requestCount, 1)

	var err error
	var rCode int
	var answer dnsproxy.RRs

	var pfx dfs = func() string { return "" }
	var txt dfs = func() string { return "" }
	var rct dfs = func() string { return dns.RcodeToString[rCode] }

	var msg *dns.Msg

	defer func() {
		dlog.Debugf(c, "%s%5d %-6s %s -> %s %s", pfx, r.Id, qts, q.Name, rct, txt)
		_ = w.WriteMsg(msg)
	}()

	if s.onlyNames {
		switch q.Qtype {
		case dns.TypeA:
			answer, rCode, err = s.cacheResolve(q)
		case dns.TypeAAAA:
			if atomic.LoadInt32(&s.recursive) == recursionDetected || q.Name == recursionCheck {
				rCode = dns.RcodeNameError
				break
			}
			q.Qtype = dns.TypeA
			answer, rCode, err = s.cacheResolve(q)
			q.Qtype = dns.TypeAAAA
			if rCode == dns.RcodeSuccess {
				// return EMPTY to indicate that dns.TypeA exists
				answer = nil
			}
		default:
			msg = new(dns.Msg)
			msg.SetRcode(r, dns.RcodeNotImplemented)
			return
		}
	} else {
		if !dnsproxy.SupportedType(q.Qtype) {
			msg = new(dns.Msg)
			msg.SetRcode(r, dns.RcodeNotImplemented)
			return
		}
		answer, rCode, err = s.cacheResolve(q)
	}

	if err == nil && rCode == dns.RcodeSuccess {
		msg = new(dns.Msg)
		msg.SetRcode(r, rCode)
		msg.Answer = answer
		msg.Authoritative = true
		// mac dns seems to fallback if you don't
		// support recursion, if you have more than a
		// single dns server, this will prevent us
		// from intercepting all queries
		msg.RecursionAvailable = true
		txt = func() string { return answer.String() }
		return
	}

	// The recursion check query, or queries that end with the cluster domain name, are not dispatched to the
	// fallback DNS-server.
	if s.fallbackPool == nil || strings.HasPrefix(q.Name, recursionCheck) || strings.HasSuffix(q.Name, s.clusterDomain) {
		if err == nil {
			rCode = dns.RcodeNameError
		} else {
			rCode = dns.RcodeServerFailure
			if errors.Is(err, context.DeadlineExceeded) {
				txt = func() string { return "timeout" }
			} else {
				txt = err.Error
			}
		}
		msg = new(dns.Msg)
		msg.SetRcode(r, rCode)
		return
	}

	pfx = func() string { return fmt.Sprintf("(%s) ", s.fallbackPool.RemoteAddr()) }
	dc := &dns.Client{Net: "udp", Timeout: s.config.LookupTimeout.AsDuration()}
	msg, _, err = s.fallbackPool.Exchange(c, dc, r)
	if err != nil {
		msg = new(dns.Msg)
		rCode = dns.RcodeServerFailure
		txt = err.Error
		if err, ok := err.(net.Error); ok {
			switch {
			case err.Timeout():
				txt = func() string { return "timeout" }
			case err.Temporary(): //nolint:staticcheck // err.Temporary is deprecated
				rCode = dns.RcodeRefused
			default:
			}
		}
		msg.SetRcode(r, rCode)
	} else {
		rCode = msg.Rcode
		txt = func() string { return dnsproxy.RRs(msg.Answer).String() }
		// When the fallback fails an AAAA, we must look for a successful A, and vice versa. If a hit of
		// the other type is found, then the returned value must be EMPTY here instead of an NXNAME
		if rCode == dns.RcodeNameError {
			var counterType uint16
			switch q.Qtype {
			case dns.TypeA:
				counterType = dns.TypeAAAA
			case dns.TypeAAAA:
				counterType = dns.TypeA
			default:
				return
			}
			if _, ok := s.cache.Load(cacheKey{name: q.Name, qType: counterType}); ok {
				rCode = dns.RcodeSuccess
				msg.Rcode = rCode
				msg.Answer = []dns.RR{}
				txt = func() string { return "EMPTY" }
			}
		}
	}
}

// dnsTTL is the number of seconds that a found DNS record should be allowed to live in the callers cache. We
// keep this low to avoid such caching.
const dnsTTL = 4

func (s *Server) resolveQuery(q *dns.Question, dv *cacheEntry) (dnsproxy.RRs, int, error) {
	atomic.StoreInt32(&dv.currentQType, int32(q.Qtype))
	defer func() {
		atomic.StoreInt32(&dv.currentQType, int32(dns.TypeNone))
		close(dv.wait)
	}()

	var err error
	dv.answer, dv.rCode, err = s.resolve(s.ctx, q)
	if err != nil || dv.rCode != dns.RcodeSuccess {
		s.cache.Delete(cacheKey{name: q.Name, qType: q.Qtype}) // Don't cache unless the lookup succeeded.
		return nil, dv.rCode, err
	}

	// Return a result for the correct query type. The result will be nil (nxdomain) if nothing was found. It might
	// also be empty if no RRs were found for the given query type and that is OK.
	// See https://datatracker.ietf.org/doc/html/rfc4074#section-3
	return copyRRs(dv.answer, q.Qtype), dv.rCode, err
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
