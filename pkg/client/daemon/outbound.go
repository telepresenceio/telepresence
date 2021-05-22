package daemon

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
)

const kubernetesZone = "cluster.local"

type IPs []net.IP

func (ips IPs) String() string {
	nips := len(ips)
	switch nips {
	case 0:
		return ""
	case 1:
		return ips[0].String()
	default:
		sb := strings.Builder{}
		sb.WriteString(ips[0].String())
		for i := 1; i < nips; i++ {
			sb.WriteByte(',')
			sb.WriteString(ips[i].String())
		}
		return sb.String()
	}
}

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
	domains    map[string]IPs
	search     []string

	// managerConfigured is closed when the traffic manager has performed
	// its first update. The DNS resolver awaits this close and so does
	// the TUN device readers and writers
	managerConfigured      chan struct{}
	closeManagerConfigured sync.Once

	// dnsConfigured is closed when the dnsWorker has configured
	// the dnsServer.
	dnsConfigured chan struct{}

	kubeDNS     chan net.IP
	onceKubeDNS sync.Once

	// The domainsLock locks usage of namespaces, domains, and search
	domainsLock sync.RWMutex

	// Lock preventing concurrent calls to setSearchPath
	searchPathLock sync.Mutex

	setSearchPathFunc func(c context.Context, paths []string)

	work chan func(context.Context) error
}

// splitToUDPAddr splits the given address into an UDPAddr. It's
// an  error if the address is based on a hostname rather than an IP.
func splitToUDPAddr(netAddr net.Addr) (*net.UDPAddr, error) {
	addr := netAddr.String()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	nsIP := net.ParseIP(host)
	if nsIP == nil {
		return nil, fmt.Errorf("host of address %q is not an IP address", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("port of address %s is not an integer", addr)
	}
	return &net.UDPAddr{IP: nsIP, Port: port}, nil
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

	var dnsIP net.IP
	if dnsIP = net.ParseIP(dnsIPStr); dnsIP != nil {
		if ip4 := dnsIP.To4(); ip4 != nil {
			dnsIP = ip4
		}
	}
	ret := &outbound{
		dnsListener:       listener,
		dnsIP:             dnsIP,
		noSearch:          noSearch,
		namespaces:        make(map[string]struct{}),
		domains:           make(map[string]IPs),
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

func (o *outbound) resolveNoSearch(query string) []net.IP {
	query = strings.ToLower(query)
	ds := strings.Split(query, ".")
	dl := len(ds) - 1 // -1 to encounter for ending dot.
	if dl < 1 {
		// bogus name, give up.
		return nil
	}
	rhsDomain := ds[dl-1]

	o.domainsLock.RLock()
	defer o.domainsLock.RUnlock()
	if dl == 2 && rhsDomain == tel2SubDomain { // only for single label names with namespace, hence dl == 2
		return o.resolveWithSearchLocked(ds[0] + ".")
	}
	if _, ok := o.namespaces[rhsDomain]; ok {
		// This is a <subdomain>.<namespace> query
		query += "svc.cluster.local."
	}
	ips := o.domains[query]
	return shuffleIPs(ips)
}

func (o *outbound) resolveWithSearchLocked(n string) []net.IP {
	for _, p := range o.search {
		if ips, ok := o.domains[n+p]; ok {
			return shuffleIPs(ips)
		}
	}
	return nil
}

// Since headless and externalName services can have multiple IPs,
// we return a shuffled list of the IPs if there are more than one.
func shuffleIPs(ips IPs) IPs {
	switch lenIPs := len(ips); lenIPs {
	case 0:
		return IPs{}
	case 1:
	default:
		// If there are multiple elements in the slice, we shuffle the
		// order so it's not the same each time
		rand.Shuffle(lenIPs, func(i, j int) {
			ips[i], ips[j] = ips[j], ips[i]
		})
	}
	return ips
}

func (o *outbound) setManagerInfo(c context.Context, info *rpc.ManagerInfo) error {
	defer o.closeManagerConfigured.Do(func() {
		close(o.managerConfigured)
	})
	return o.router.setManagerInfo(c, info)
}

func (o *outbound) update(_ context.Context, table *rpc.Table) (err error) {
	// o.proxy.SetSocksPort(table.SocksPort)
	ips := make(map[IPKey]struct{}, len(table.Routes))
	domains := make(map[string]IPs)
	for _, route := range table.Routes {
		dIps := make(IPs, 0, len(route.Ips))
		for _, ipStr := range route.Ips {
			if ip := net.ParseIP(ipStr); ip != nil {
				// ParseIP returns ipv4 that are 16 bytes long. Normalize them into 4 bytes
				if ip4 := ip.To4(); ip4 != nil {
					ip = ip4
				}
				dIps = append(dIps, ip)
				ips[IPKey(ip)] = struct{}{}
			}
		}
		if dn := route.Name; dn != "" {
			dn = strings.ToLower(dn) + "."
			domains[dn] = append(domains[dn], dIps...)
		}
	}
	o.work <- func(c context.Context) error {
		return o.doUpdate(c, domains, ips)
	}
	return nil
}

func (o *outbound) noMoreUpdates() {
	close(o.work)
}

func (ips IPs) uniqueSorted() IPs {
	sort.Slice(ips, func(i, j int) bool {
		return bytes.Compare(ips[i], ips[j]) < 0
	})
	var prev net.IP
	last := len(ips) - 1
	for i := 0; i <= last; i++ {
		s := ips[i]
		if s.Equal(prev) {
			copy(ips[i:], ips[i+1:])
			last--
			i--
		} else {
			prev = s
		}
	}
	return ips[:last+1]
}

func (o *outbound) doUpdate(c context.Context, domains map[string]IPs, table map[IPKey]struct{}) error {
	// We're updating routes. Make sure DNS waits until the new answer
	// is ready, i.e. don't serve old answers.
	o.domainsLock.Lock()
	defer o.domainsLock.Unlock()

	dnsChanged := false
	for k, ips := range o.domains {
		if _, ok := domains[k]; !ok {
			dnsChanged = true
			dlog.Debugf(c, "CLEAR %s -> %s", k, ips)
			delete(o.domains, k)
		}
	}

	var kubeDNS net.IP
	for k, nIps := range domains {
		// Ensure that all added entries contain unique, sorted, and non-empty IPs
		nIps = nIps.uniqueSorted()
		if len(nIps) == 0 {
			delete(domains, k)
			continue
		}
		domains[k] = nIps
		if oIps, ok := o.domains[k]; ok {
			if ok = len(nIps) == len(oIps); ok {
				for i, ip := range nIps {
					if ok = ip.Equal(oIps[i]); !ok {
						break
					}
				}
			}
			if !ok {
				dnsChanged = true
				dlog.Debugf(c, "REPLACE %s -> %s with %s", oIps, k, nIps)
			}
		} else {
			dnsChanged = true
			if k == "kube-dns.kube-system.svc.cluster.local." && len(nIps) >= 1 {
				kubeDNS = nIps[0]
			}
			dlog.Debugf(c, "STORE %s -> %s", k, nIps)
		}
	}

	// Operate on the copy of the current table and the new table
	ipsChanged := false
	oldIPs := o.router.snapshot()
	for ip := range table {
		if o.router.add(c, ip) {
			ipsChanged = true
		}
		delete(oldIPs, ip)
	}

	for ip := range oldIPs {
		if o.router.clear(c, ip) {
			ipsChanged = true
		}
	}

	if ipsChanged {
		if err := o.router.flush(c, kubeDNS); err != nil {
			dlog.Errorf(c, "flush: %v", err)
		}
	}

	if dnsChanged {
		o.domains = domains
		dns.Flush(c)
	}

	if kubeDNS != nil {
		o.onceKubeDNS.Do(func() { o.kubeDNS <- kubeDNS })
	}
	return nil
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
