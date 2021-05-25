package daemon

import (
	"context"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	dns2 "github.com/miekg/dns"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const kubernetesZone = "cluster.local"

// outbound does stuff, idk, I didn't write it.
//
// A zero outbound is invalid; you must use newOutbound.
type outbound struct {
	dnsListener net.PacketConn
	dnsIP       net.IP
	noSearch    bool
	router      *tunRouter

	// Namespaces, accessible using <service-name>.<namespace-name>
	namespaces map[string]struct{}
	domains    map[string]iputil.IPs
	search     []string

	// managerConfigured is closed when the traffic manager has performed
	// its first update. The DNS resolver awaits this close and so does
	// the TUN device readers and writers
	managerConfigured      chan struct{}
	closeManagerConfigured sync.Once

	// dnsConfigured is closed when the dnsWorker has configured
	// the dnsServer.
	dnsConfigured chan struct{}

	kubeDNS chan net.IP

	// The domainsLock locks usage of namespaces, domains, and search
	domainsLock sync.RWMutex

	// Lock preventing concurrent calls to setSearchPath
	searchPathLock sync.Mutex

	setSearchPathFunc func(c context.Context, paths []string)

	work chan func(context.Context) error

	// dnsQueriesInProgress unique set of DNS queries currently in progress.
	dnsAInProgress    map[string]struct{}
	dnsAAAAInProgress map[string]struct{}
	dnsQueriesLock    sync.Mutex
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

// newOutbound returns a new properly initialized outbound object.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
func newOutbound(c context.Context, dnsIPStr string, noSearch bool) (*outbound, error) {
	lc := &net.ListenConfig{}
	listener, err := lc.ListenPacket(c, "udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	// seed random generator (used when shuffling IPs)
	rand.Seed(time.Now().UnixNano())

	ret := &outbound{
		dnsListener:       listener,
		dnsIP:             iputil.Parse(dnsIPStr),
		noSearch:          noSearch,
		namespaces:        make(map[string]struct{}),
		domains:           make(map[string]iputil.IPs),
		dnsAInProgress:    make(map[string]struct{}),
		dnsAAAAInProgress: make(map[string]struct{}),
		search:            []string{""},
		work:              make(chan func(context.Context) error),
		dnsConfigured:     make(chan struct{}),
		managerConfigured: make(chan struct{}),
		kubeDNS:           make(chan net.IP, 1),
	}

	if ret.router, err = newTunRouter(ret.managerConfigured); err != nil {
		return nil, err
	}
	return ret, nil
}

// routerServerWorker starts the TUN router and reads from the work queue of firewall config
// changes that is written to by the 'Update' gRPC call.
func (o *outbound) routerServerWorker(c context.Context) (err error) {
	go func() {
		// No need to select between <-o.work and <-c.Done(); o.work will get closed when we start
		// shutting down.
		for f := range o.work {
			if c.Err() == nil {
				// As long as we're not shutting down, keep doing work.  (If we are shutting
				// down, do nothing but don't 'break'; keep draining the queue.)
				if err = f(c); err != nil {
					dlog.Error(c, err)
				}
			}
		}
	}()
	return o.router.run(c)
}

// On a MacOS, Docker uses its own search-path for single label names. This means that the search path that is declared
// in the MacOS resolver is ignored although the rest of the DNS-resolution works OK. Since the search-path is likely to
// change during a session, a stable fake domain is needed to emulate the search-path. That fake-domain can then be used
// in the search path declared in the Docker config. The "tel2-search" domain fills this purpose and a request for
// "<single label name>.tel2-search." will be resolved as "<single label name>." using the search path of this resolver.
const tel2SubDomain = "tel2-search"
const dotTel2SubDomain = "." + tel2SubDomain
const dotKubernetesZone = "." + kubernetesZone

var localhostIPv6 = []net.IP{{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}}
var localhostIPv4 = []net.IP{{127, 0, 0, 1}}

func (o *outbound) resolveInCluster(c context.Context, qType uint16, query string) []net.IP {
	var inProgress map[string]struct{}
	switch qType {
	case dns2.TypeA:
		inProgress = o.dnsAInProgress
	case dns2.TypeAAAA:
		inProgress = o.dnsAAAAInProgress
	default:
		return nil
	}

	query = query[:len(query)-1]
	query = strings.ToLower(query) // strip of trailing dot
	query = strings.TrimSuffix(query, dotTel2SubDomain)

	if query == "localhost" {
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

	// We don't care about queries to the kubernetes zone unless they have at least two additional elements.
	if strings.HasSuffix(query, dotKubernetesZone) && strings.Count(query, ".") < 3 {
		return nil
	}

	o.dnsQueriesLock.Lock()
	for qip := range inProgress {
		if strings.HasPrefix(query, qip) {
			// This is most likely a recursion caused by the query in progress. This happens when a cluster
			// runs locally on the same host as Telepresence and falls back to use the DNS of that host when
			// the query cannot be resolved in the cluster. Sending that query to the traffic-manager is
			// pointless so we end the recursion here.
			o.dnsQueriesLock.Unlock()
			return nil
		}
	}
	inProgress[query] = struct{}{}
	o.dnsQueriesLock.Unlock()

	defer func() {
		o.dnsQueriesLock.Lock()
		delete(inProgress, query)
		o.dnsQueriesLock.Unlock()
	}()
	dlog.Debugf(c, "LookupHost %s", query)
	response, err := o.router.managerClient.LookupHost(c, &manager.LookupHostRequest{
		Session: o.router.session,
		Host:    query,
	})
	if err != nil {
		dlog.Error(c, err)
		return nil
	}
	if len(response.Ips) == 0 {
		return nil
	}
	ips := make(iputil.IPs, 0, len(response.Ips))
	for _, ip := range response.Ips {
		if qType == dns2.TypeA {
			if ip4 := net.IP(ip).To4(); ip4 != nil {
				ips = append(ips, ip4)
			}
		} else {
			if len(ip) == 16 {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func (o *outbound) setInfo(c context.Context, info *rpc.OutboundInfo) error {
	defer o.closeManagerConfigured.Do(func() {
		close(o.managerConfigured)
	})
	if err := o.router.setOutboundInfo(c, info); err != nil {
		return err
	}
	o.kubeDNS <- info.KubeDnsIp
	return nil
}

func (o *outbound) noMoreUpdates() {
	close(o.work)
}

// SetSearchPath updates the DNS search path used by the resolver
func (o *outbound) setSearchPath(c context.Context, paths []string) {
	select {
	case <-c.Done():
	case <-o.dnsConfigured:
		o.searchPathLock.Lock()
		defer o.searchPathLock.Unlock()
		o.setSearchPathFunc(c, paths)
		dns.Flush(c)
	}
}
