package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type Resolver func(context.Context, *dns.Question) ([]dns.RR, error)

// recursionCheck is a special host name in a well known namespace that isn't expected to exist. It
// is used once for determining if the cluster's DNS resolver will call the Telepresence DNS resolver
// recursively. This is common when the cluster is running on the local host (k3s in docker for instance).
const recursionCheck = "tel2-recursion-check.kube-system."

// defaultClusterDomain used unless traffic-manager reports otherwise
const defaultClusterDomain = "cluster.local."

type FallbackPool interface {
	Exchange(context.Context, *dns.Client, *dns.Msg) (*dns.Msg, time.Duration, error)
	RemoteAddr() string
	LocalAddrs() []net.Addr
}

const (
	recursionUnknown = int32(iota)
	recursionNotDetected
	recursionDetected
	recursionTestInProgress
)

type listeners struct {
	tcpListener net.Listener
	udpListener net.PacketConn
}

// Server is a DNS server which implements the github.com/miekg/dns Handler interface
type Server struct {
	ctx          context.Context // necessary to make logging work in ServeDNS function
	resolve      Resolver
	requestCount int64
	cache        sync.Map
	recursive    int32 // one of the recursionXXX constants declared above (unique type avoided because it just gets messy with the atomic calls)
	cacheResolve func(*dns.Question) ([]dns.RR, error)

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

	// Function that sends a lookup requrest to the traffic-manager
	clusterLookup Resolver
}

type cacheEntry struct {
	created      time.Time
	currentQType int32 // will be set to the current qType during call to cluster
	answer       []dns.RR
	wait         chan struct{}
}

// cacheTTL is the time to live for an entry in the local DNS cache.
const cacheTTL = 60 * time.Second

func (dv *cacheEntry) expired() bool {
	return time.Since(dv.created) > cacheTTL
}

// NewServer returns a new dns.Server
func NewServer(config *rpc.DNSConfig, clusterLookup Resolver) *Server {
	if config == nil {
		config = &rpc.DNSConfig{}
	}
	if len(config.ExcludeSuffixes) == 0 {
		config.ExcludeSuffixes = []string{
			".arpa",
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
	}
	s.cacheResolve = s.resolveWithRecursionCheck
	return s
}

// tel2SubDomain aims to fix a search-path problem when using Docker on non-linux systems where
// Docker uses its own search-path for single label names. This means that the search path that
// is declared in the macOS resolver is ignored although the rest of the DNS-resolution works OK.
// Since the search-path is likely to change during a session, a stable fake domain is needed to
// emulate the search-path. That fake-domain can then be used in the search path declared in the
// Docker config.
//
// The "tel2-search" domain fills this purpose and a request for "<single label name>.tel2-search."
// will be resolved as "<single label name>." using the search path of this resolver.
const tel2SubDomain = "tel2-search"
const tel2SubDomainDot = tel2SubDomain + "."
const wpadDot = "wpad."

var localhostIPv4 = net.IP{127, 0, 0, 1}
var localhostIPv6 = net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}

func (s *Server) shouldDoClusterLookup(query string) bool {
	n := strings.Count(query, ".")
	if strings.HasSuffix(query, "."+s.clusterDomain) {
		switch {
		case n < 4:
			// Reject "<label>.cluster.local."
			return false
		case (n == 4 || n == 5) && strings.HasPrefix(query, wpadDot) && strings.Contains(query, ".svc."):
			// Reject "wpad.svc.cluster.local." and "wpad.<namespace>.svc.cluster.local."
			return false
		}
	} else {
		switch {
		case n == 1 && query == wpadDot:
			// Reject "wpad."
			return false
		case n == 2 && strings.HasPrefix(query, wpadDot):
			// Reject "wpad.<namespace>."
			return false
		}
	}

	query = query[:len(query)-1] // skip last dot

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

func (s *Server) resolveInCluster(c context.Context, q *dns.Question) (result []dns.RR, err error) {
	query := q.Name
	query = strings.ToLower(query)
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
			return []dns.RR{&dns.A{
				Hdr: dns.RR_Header{},
				A:   localhostIPv4,
			}}, nil
		case dns.TypeAAAA:
			return []dns.RR{&dns.AAAA{
				Hdr:  dns.RR_Header{},
				AAAA: localhostIPv6,
			}}, nil
		}
	}

	if !s.shouldDoClusterLookup(query) {
		return nil, nil
	}

	// Give the cluster lookup a reasonable timeout.
	c, cancel := context.WithTimeout(c, s.config.LookupTimeout.AsDuration())
	defer cancel()

	result, err = s.clusterLookup(c, q)
	if err != nil {
		return nil, client.CheckTimeout(c, err)
	}
	// Keep the TTLs of requests resolved in the cluster low. We
	// cache them locally anyway, but our cache is flushed when things are
	// intercepted or the namespaces change.
	for _, rr := range result {
		if h := rr.Header(); h != nil {
			h.Ttl = dnsTTL
		}
	}
	return result, nil
}

func (s *Server) GetConfig() *rpc.DNSConfig {
	dnsConfig := &rpc.DNSConfig{}
	if s.config != nil {
		dnsConfig.LocalIp = s.config.LocalIp
		dnsConfig.ExcludeSuffixes = s.config.ExcludeSuffixes
		dnsConfig.IncludeSuffixes = s.config.IncludeSuffixes
		dnsConfig.LookupTimeout = s.config.LookupTimeout
	}
	return dnsConfig
}

func (s *Server) SetClusterDomainAndDNS(domain string, dnsIP net.IP) {
	s.clusterDomain = domain
	if s.config == nil {
		s.config = &rpc.DNSConfig{}
	}
	if s.config.RemoteIp == nil {
		s.config.RemoteIp = dnsIP
	}
}

// SetSearchPath updates the DNS search path used by the resolver
func (s *Server) SetSearchPath(ctx context.Context, paths, namespaces []string) {
	// Provide direct access to intercepted namespaces
	for _, ns := range namespaces {
		paths = append(paths, ns+".svc."+s.clusterDomain)
	}
	select {
	case <-ctx.Done():
	case s.searchPathCh <- paths:
	}
}

func newLocalTCPListener(c context.Context) (net.Listener, error) {
	lc := &net.ListenConfig{}
	return lc.Listen(c, "tcp", "127.0.0.1:0")
}

func newLocalUDPListener(c context.Context) (net.PacketConn, error) {
	lc := &net.ListenConfig{}
	return lc.ListenPacket(c, "udp", "127.0.0.1:0")
}

func (s *Server) processSearchPaths(g *dgroup.Group, processor func(context.Context, []string, vif.Device) error, dev vif.Device) {
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
					if atomic.LoadInt32(&s.recursive) == recursionUnknown {
						for _, p := range prevPaths {
							if p == "kube-system" {
								if atomic.CompareAndSwapInt32(&s.recursive, recursionUnknown, recursionTestInProgress) {
									go s.performRecursionCheck(c)
								}
								break
							}
						}
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

func copyRRs(rrs []dns.RR, qType uint16) []dns.RR {
	if len(rrs) == 0 {
		return rrs
	}
	cp := make([]dns.RR, 0, len(rrs))
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
func (s *Server) resolveThruCache(q *dns.Question) ([]dns.RR, error) {
	newDv := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	key := cacheKey{name: q.Name, qType: q.Qtype}
	if v, loaded := s.cache.LoadOrStore(key, newDv); loaded {
		oldDv := v.(*cacheEntry)
		if atomic.LoadInt32(&s.recursive) == recursionDetected && atomic.LoadInt32(&oldDv.currentQType) == int32(q.Qtype) {
			// We have to assume that this is a recursion from the cluster.
			return nil, nil
		}
		<-oldDv.wait
		if !oldDv.expired() {
			return copyRRs(oldDv.answer, q.Qtype), nil
		}
		s.cache.Store(key, newDv)
	}
	return s.resolveQuery(q, newDv)
}

// resolveWithRecursionCheck is a special version of resolveThruCache which is only used until the
// recursionCheck query has completed, and it has been determined whether a query that is propagated
// to the cluster will recurse back to this resolver or not.
func (s *Server) resolveWithRecursionCheck(q *dns.Question) ([]dns.RR, error) {
	newDv := &cacheEntry{wait: make(chan struct{}), created: time.Now()}
	key := cacheKey{name: q.Name, qType: q.Qtype}
	if v, loaded := s.cache.LoadOrStore(key, newDv); loaded {
		oldDv := v.(*cacheEntry)
		if atomic.LoadInt32(&oldDv.currentQType) == int32(q.Qtype) {
			if q.Name == recursionCheck {
				atomic.StoreInt32(&s.recursive, recursionDetected)
			}
			if atomic.LoadInt32(&s.recursive) == recursionDetected {
				return nil, nil
			}
		}
		<-oldDv.wait
		if !oldDv.expired() {
			return copyRRs(oldDv.answer, q.Qtype), nil
		}
		s.cache.Store(key, newDv)
	}

	answer, err := s.resolveQuery(q, newDv)
	if q.Name == recursionCheck {
		if atomic.LoadInt32(&s.recursive) == recursionDetected {
			dlog.Debug(s.ctx, "DNS resolver is recursive")
		} else {
			atomic.StoreInt32(&s.recursive, recursionNotDetected)
			dlog.Debug(s.ctx, "DNS resolver is not recursive")
		}
		s.cacheResolve = s.resolveThruCache
	}
	return answer, err
}

// dfs is a func that implements the fmt.Stringer interface. Used in log statements to ensure
// that the function isn't evaluated until the log output is formatted (which will happen only
// if the given loglevel is enabled).
type dfs func() string

func (d dfs) String() string {
	return d()
}

func (s *Server) performRecursionCheck(c context.Context) {
	const maxRetry = 10
	defer dlog.Debug(c, "Recursion check finished")
	rc := strings.TrimSuffix(recursionCheck, ".")
	dlog.Debugf(c, "Performing initial recursion check with %s", rc)
	i := 0
	for ; i < maxRetry; i++ {
		_, err := net.DefaultResolver.LookupIP(c, "ip4", rc)
		if err != nil {
			if derr, ok := err.(*net.DNSError); ok && derr.IsNotFound {
				err = nil
			}
		}
		if err != nil {
			dlog.Errorf(c, "recursion check ended with %v", err)
		}
		if atomic.LoadInt32(&s.recursive) != recursionTestInProgress {
			return
		}
		// Check didn't hit our resolver. Try again after a second
		dtime.SleepWithContext(c, time.Second)

		// Check that the resolver didn't get hit during our wait. We don't want
		// to retry if it did, because that will give the false impression that
		// the resolver is recursive.
		if atomic.LoadInt32(&s.recursive) != recursionTestInProgress {
			return
		}
		dlog.Debug(c, "retrying recursion check")
	}
	if i == maxRetry {
		dlog.Errorf(c, "recursion check failed. The DNS isn't working properly")
	}
}

// ServeDNS is an implementation of github.com/miekg/dns Handler.ServeDNS.
func (s *Server) serveDNS(w dns.ResponseWriter, r *dns.Msg, fallbackPool FallbackPool) {
	c := s.ctx
	defer func() {
		// Closing the response tells the DNS service to terminate
		if c.Err() != nil {
			_ = w.Close()
		}
	}()

	q := &r.Question[0]
	atomic.AddInt64(&s.requestCount, 1)

	answerString := func(a []dns.RR) string {
		if a == nil {
			return ""
		}
		switch len(a) {
		case 0:
			return "EMPTY"
		case 1:
			return a[0].String()
		default:
			return fmt.Sprintf("%v", a)
		}
	}

	qts := dns.TypeToString[q.Qtype]
	answer, err := s.cacheResolve(q)
	var rc int
	var pfx dfs = func() string { return "" }
	var txt dfs = func() string { return "" }
	var rct dfs = func() string { return dns.RcodeToString[rc] }

	var msg *dns.Msg

	defer func() {
		dlog.Debugf(c, "%s%-6s %s -> %s %s", pfx, qts, q.Name, rct, txt)
		_ = w.WriteMsg(msg)
	}()

	if err == nil && answer != nil {
		rc = dns.RcodeSuccess
		msg = new(dns.Msg)
		msg.SetReply(r)
		msg.Answer = answer
		msg.Authoritative = true
		// mac dns seems to fallback if you don't
		// support recursion, if you have more than a
		// single dns server, this will prevent us
		// from intercepting all queries
		msg.RecursionAvailable = true
		txt = func() string { return answerString(msg.Answer) }
		return
	}

	// The recursion check query, or queries that end with the cluster domain name, are not dispatched to the
	// fallback DNS-server.
	if fallbackPool == nil || strings.HasPrefix(q.Name, recursionCheck) || strings.HasSuffix(q.Name, s.clusterDomain) {
		if err == nil {
			rc = dns.RcodeNameError
		} else {
			rc = dns.RcodeServerFailure
			if errors.Is(err, context.DeadlineExceeded) {
				txt = func() string { return "timeout" }
			} else {
				txt = err.Error
			}
		}
		msg = new(dns.Msg)
		msg.SetRcode(r, rc)
		return
	}

	pfx = func() string { return fmt.Sprintf("(%s) ", fallbackPool.RemoteAddr()) }
	dc := &dns.Client{Net: "udp", Timeout: s.config.LookupTimeout.AsDuration()}
	msg, _, err = fallbackPool.Exchange(c, dc, r)
	if err != nil {
		msg = new(dns.Msg)
		rc = dns.RcodeServerFailure
		txt = err.Error
		if err, ok := err.(net.Error); ok {
			switch {
			case err.Timeout():
				txt = func() string { return "timeout" }
			case err.Temporary(): //nolint:staticcheck // err.Temporary is deprecated
				rc = dns.RcodeRefused
			default:
			}
		}
		msg.SetRcode(r, rc)
	} else {
		rc = msg.Rcode
		txt = func() string { return answerString(msg.Answer) }
	}
}

// dnsTTL is the number of seconds that a found DNS record should be allowed to live in the callers cache. We
// keep this low to avoid such caching.
const dnsTTL = 4

func (s *Server) resolveQuery(q *dns.Question, dv *cacheEntry) ([]dns.RR, error) {
	atomic.StoreInt32(&dv.currentQType, int32(q.Qtype))
	defer func() {
		atomic.StoreInt32(&dv.currentQType, int32(dns.TypeNone))
		close(dv.wait)
	}()

	rrs, err := s.resolve(s.ctx, q)
	if err != nil || len(rrs) == 0 {
		s.cache.Delete(cacheKey{name: q.Name, qType: q.Qtype}) // Don't cache unless the entry is found.
		return nil, err
	}
	dv.answer = rrs

	// Return a result for the correct query type. The result will be nil (nxdomain) if nothing was found. It might
	// also be empty if no RRs were found for the given query type and that is OK.
	// See https://datatracker.ietf.org/doc/html/rfc4074#section-3
	return copyRRs(dv.answer, q.Qtype), err
}

type handler func(w dns.ResponseWriter, r *dns.Msg)

func (h handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	h(w, r)
}

// Run starts the DNS server(s) and waits for them to end
func (s *Server) Run(c context.Context, initDone chan<- struct{}, listeners *listeners, fallbackPoolUDP FallbackPool, fallbackPoolTCP FallbackPool, resolve Resolver) error {
	s.ctx = c
	s.resolve = resolve

	udpSrv := &dns.Server{
		PacketConn: listeners.udpListener,
		Handler: handler(func(w dns.ResponseWriter, r *dns.Msg) {
			s.serveDNS(w, r, fallbackPoolUDP)
		}),
		ReadTimeout: time.Second,
	}
	tcpSrv := &dns.Server{
		Listener: listeners.tcpListener,
		Handler: handler(func(w dns.ResponseWriter, r *dns.Msg) {
			s.serveDNS(w, r, fallbackPoolTCP)
		}),
		ReadTimeout: time.Second,
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go(listeners.udpListener.LocalAddr().String(), func(c context.Context) error {
		go func() {
			<-c.Done()
			dlog.Debugf(c, "Shutting down DNS UDP-server")
			_ = udpSrv.ShutdownContext(dcontext.HardContext(c))
		}()
		return udpSrv.ActivateAndServe()
	})
	g.Go(listeners.tcpListener.Addr().String(), func(c context.Context) error {
		go func() {
			<-c.Done()
			dlog.Debugf(c, "Shutting down DNS TCP-server")
			_ = tcpSrv.ShutdownContext(dcontext.HardContext(c))
		}()
		return tcpSrv.ActivateAndServe()
	})
	close(initDone)
	return g.Wait()
}

func dnsListeners(c context.Context) (*listeners, error) {
	ls := listeners{}
	var err error
	if ls.udpListener, err = newLocalUDPListener(c); err != nil {
		return nil, err
	}
	if ls.tcpListener, err = newLocalTCPListener(c); err != nil {
		return nil, err
	}
	return &ls, nil
}
