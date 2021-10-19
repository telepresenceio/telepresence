package daemon

import (
	"context"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	dns2 "github.com/miekg/dns"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type awaitLookupResult struct {
	done   chan struct{}
	result iputil.IPs
}

// outbound does stuff, idk, I didn't write it.
//
// A zero outbound is invalid; you must use newOutbound.
type outbound struct {
	noSearch bool
	router   *tunRouter

	// Namespaces, accessible using <service-name>.<namespace-name>
	namespaces map[string]struct{}
	domains    map[string]struct{}
	search     []string

	// The domainsLock locks usage of namespaces, domains, and search
	domainsLock sync.RWMutex

	searchPathCh chan []string

	// dnsQueriesInProgress unique set of DNS queries currently in progress.
	dnsInProgress  map[string]*awaitLookupResult
	dnsQueriesLock sync.Mutex

	dnsConfig *rpc.DNSConfig

	scout chan<- scout.ScoutReport
}

func newLocalUDPListener(c context.Context) (net.PacketConn, error) {
	lc := &net.ListenConfig{}
	return lc.ListenPacket(c, "udp", "127.0.0.1:0")
}

// newOutbound returns a new properly initialized outbound object.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
func newOutbound(c context.Context, dnsIPStr string, noSearch bool, scout chan<- scout.ScoutReport) (*outbound, error) {
	// seed random generator (used when shuffling IPs)
	rand.Seed(time.Now().UnixNano())

	ret := &outbound{
		dnsConfig: &rpc.DNSConfig{
			LocalIp: iputil.Parse(dnsIPStr),
		},
		noSearch:      noSearch,
		namespaces:    make(map[string]struct{}),
		domains:       make(map[string]struct{}),
		dnsInProgress: make(map[string]*awaitLookupResult),
		search:        []string{""},
		searchPathCh:  make(chan []string, 5),
		scout:         scout,
	}

	var err error
	if ret.router, err = newTunRouter(c); err != nil {
		return nil, err
	}
	return ret, nil
}

// On a macOS, Docker uses its own search-path for single label names. This means that the search path that is declared
// in the macOS resolver is ignored although the rest of the DNS-resolution works OK. Since the search-path is likely to
// change during a session, a stable fake domain is needed to emulate the search-path. That fake-domain can then be used
// in the search path declared in the Docker config. The "tel2-search" domain fills this purpose and a request for
// "<single label name>.tel2-search." will be resolved as "<single label name>." using the search path of this resolver.
const tel2SubDomain = "tel2-search"
const tel2SubDomainDot = tel2SubDomain + "."

var localhostIPv6 = []net.IP{{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}
var localhostIPv4 = []net.IP{{127, 0, 0, 1}}

func (o *outbound) shouldDoClusterLookup(query string) bool {
	if strings.HasSuffix(query, "."+o.router.clusterDomain) && strings.Count(query, ".") < 4 {
		return false
	}

	query = query[:len(query)-1] // skip last dot

	// Always include configured includeSuffixes
	for _, sfx := range o.dnsConfig.IncludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return true
		}
	}

	// Skip configured excludeSuffixes
	for _, sfx := range o.dnsConfig.ExcludeSuffixes {
		if strings.HasSuffix(query, sfx) {
			return false
		}
	}
	return true
}

func (o *outbound) resolveInCluster(c context.Context, qType uint16, query string) (results []net.IP) {
	query = strings.ToLower(query)
	query = strings.TrimSuffix(query, tel2SubDomainDot)

	if query == "localhost." {
		// BUG(lukeshu): I have no idea why a lookup
		// for localhost even makes it to here on my
		// home WiFi when connecting to a k3sctl
		// cluster (but not a kubernaut.io cluster).
		// But it does, so I need this in order to be
		// productive at home.  We should really
		// root-cause this, because it's weird.
		if qType == dns2.TypeAAAA {
			return localhostIPv6
		}
		return localhostIPv4
	}

	if !o.shouldDoClusterLookup(query) {
		return nil
	}
	// Don't report queries that won't be resolved in-cluster, since that'll report every single DNS query on the user's machine
	defer func() {
		r := scout.ScoutReport{
			Action: "incluster_dns_query",
			Metadata: map[string]interface{}{
				"had_results": results != nil,
			},
		}
		// Post to scout channel but never block if it's full
		select {
		case o.scout <- r:
		default:
		}
	}()

	var firstLookupResult *awaitLookupResult
	o.dnsQueriesLock.Lock()
	awaitResult := o.dnsInProgress[query]
	if awaitResult == nil {
		firstLookupResult = &awaitLookupResult{done: make(chan struct{})}
		o.dnsInProgress[query] = firstLookupResult
	}
	o.dnsQueriesLock.Unlock()

	if awaitResult != nil {
		// Wait for this query to complete. Then return its value
		select {
		case <-awaitResult.done:
			return awaitResult.result
		case <-c.Done():
			return nil
		}
	}

	// Give the cluster lookup a reasonable timeout.
	c, cancel := context.WithTimeout(c, o.dnsConfig.LookupTimeout.AsDuration())
	defer func() {
		cancel()
		o.dnsQueriesLock.Lock()
		delete(o.dnsInProgress, query)
		o.dnsQueriesLock.Unlock()
		close(firstLookupResult.done)
	}()

	queryWithNoTrailingDot := query[:len(query)-1]
	dlog.Debugf(c, "LookupHost %q", queryWithNoTrailingDot)
	response, err := o.router.managerClient.LookupHost(c, &manager.LookupHostRequest{
		Session: o.router.session,
		Host:    queryWithNoTrailingDot,
	})
	if err != nil {
		dlog.Error(c, client.CheckTimeout(c, err))
		return nil
	}
	if len(response.Ips) == 0 {
		return nil
	}
	ips := make(iputil.IPs, len(response.Ips))
	for i, ip := range response.Ips {
		ips[i] = ip
	}
	firstLookupResult.result = ips
	return ips
}

func (o *outbound) setInfo(ctx context.Context, info *rpc.OutboundInfo) error {
	if info.Dns == nil {
		info.Dns = &rpc.DNSConfig{}
	}
	if oldIP := o.dnsConfig.GetLocalIp(); len(oldIP) > 0 {
		info.Dns.LocalIp = oldIP
	}
	if len(info.Dns.ExcludeSuffixes) == 0 {
		info.Dns.ExcludeSuffixes = []string{
			".arpa",
			".com",
			".io",
			".net",
			".org",
			".ru",
		}
	}
	if info.Dns.LookupTimeout.AsDuration() <= 0 {
		info.Dns.LookupTimeout = durationpb.New(4 * time.Second)
	}
	o.dnsConfig = info.Dns
	return o.router.setOutboundInfo(ctx, info)
}

func (o *outbound) getInfo() *rpc.OutboundInfo {
	info := rpc.OutboundInfo{
		Dns: &rpc.DNSConfig{
			RemoteIp: o.router.dnsIP,
		},
	}
	if o.dnsConfig != nil {
		info.Dns.LocalIp = o.dnsConfig.LocalIp
		info.Dns.ExcludeSuffixes = o.dnsConfig.ExcludeSuffixes
		info.Dns.IncludeSuffixes = o.dnsConfig.IncludeSuffixes
		info.Dns.LookupTimeout = o.dnsConfig.LookupTimeout
	}

	if len(o.router.alsoProxySubnets) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(o.router.alsoProxySubnets))
		for i, ap := range o.router.alsoProxySubnets {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC(ap)
		}
	}

	if len(o.router.neverProxySubnets) > 0 {
		info.NeverProxySubnets = make([]*manager.IPNet, len(o.router.neverProxySubnets))
		for i, np := range o.router.neverProxySubnets {
			info.NeverProxySubnets[i] = iputil.IPNetToRPC(np.RoutedNet)
		}
	}

	return &info
}

// SetSearchPath updates the DNS search path used by the resolver
func (o *outbound) setSearchPath(ctx context.Context, paths, namespaces []string) {
	// Provide direct access to intercepted namespaces
	for _, ns := range namespaces {
		paths = append(paths, ns+".svc."+o.router.clusterDomain)
	}
	select {
	case <-ctx.Done():
	case o.searchPathCh <- paths:
	}
}

func (o *outbound) processSearchPaths(g *dgroup.Group, processor func(context.Context, []string) error) {
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
			case paths := <-o.searchPathCh:
				if len(o.searchPathCh) > 0 {
					// Only interested in the last one
					continue
				}
				if !unchanged(paths) {
					dlog.Debugf(c, "%v -> %v", prevPaths, paths)
					prevPaths = make([]string, len(paths))
					copy(prevPaths, paths)
					if err := processor(c, paths); err != nil {
						return err
					}
				}
			}
		}
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
