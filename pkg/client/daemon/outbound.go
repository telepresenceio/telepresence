package daemon

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/nat"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/proxy"
)

// outbound does stuff, idk, I didn't write it.
//
// A zero outbound is invalid; you must use newOutbound.
type outbound struct {
	dnsListener net.PacketConn
	dnsIP       string
	fallbackIP  string
	noSearch    bool
	translator  nat.FirewallRouter
	tables      map[string]*nat.Table
	tablesLock  sync.RWMutex

	// Namespaces, accessible using <service-name>.<namespace-name>
	namespaces        map[string]struct{}
	domains           map[string][]string
	domainsLock       sync.RWMutex
	setSearchPathFunc func(c context.Context, paths []string)

	search     []string
	searchLock sync.RWMutex

	overridePrimaryDNS bool

	// proxyRedirPort is the port to which we redirect translated IP requests intended for the cluster
	proxyRedirPort int

	// dnsRedirPort is the port to which we redirect dns requests.
	dnsRedirPort int

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
//
// If fallbackIP is empty, it will default to Google DNS.
func newOutbound(c context.Context, name string, dnsIP, fallbackIP string, noSearch bool) (*outbound, error) {
	lc := &net.ListenConfig{}
	listener, err := lc.ListenPacket(c, "udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	// seed random generator (used when shuffling IPs)
	rand.Seed(time.Now().UnixNano())

	ret := &outbound{
		dnsListener: listener,
		dnsIP:       dnsIP,
		fallbackIP:  fallbackIP,
		noSearch:    noSearch,
		tables:      make(map[string]*nat.Table),
		translator:  nat.NewRouter(name, net.IP{127, 0, 0, 1}),
		namespaces:  make(map[string]struct{}),
		domains:     make(map[string][]string),
		search:      []string{""},
		work:        make(chan func(context.Context) error),
	}
	ret.tablesLock.Lock() // leave it locked until firewallConfiguratorWorker unlocks it
	return ret, nil
}

// firewall2socksWorker listens on localhost:<proxyRedirPort> and forwards those connections to the connector's
// SOCKS server, for them to be forwarded to the cluster.  We count on firewallConfiguratorWorker
// having configured the host firewall to send all cluster-bound TCP connections to this port, and
// we make special syscalls/ioctls to determine where each connection was originally bound for, so
// that we know what to tell SOCKS.
func (o *outbound) firewall2socksWorker(c context.Context, onReady func()) error {
	// hmm, we may not actually need to get the original
	// destination, we could just forward each ip to a unique port
	// and either listen on that port or run port-forward
	pr, err := proxy.NewProxy(c, o.destination)
	if err != nil {
		return errors.Wrap(err, "Proxy")
	}
	o.proxyRedirPort, err = pr.ListenerPort()
	if err != nil {
		return errors.Wrap(err, "Proxy")
	}
	dlog.Debug(c, "Starting server")
	onReady()
	pr.Run(c, 10000)
	dlog.Debug(c, "Server done")
	return nil
}

// firewallConfiguratorWorker reads from the work queue of firewall config changes that is written
// to by the 'Update' gRPC call.
func (o *outbound) firewallConfiguratorWorker(c context.Context) (err error) {
	defer func() {
		o.tablesLock.Lock()
		if err2 := o.translator.Disable(dcontext.HardContext(c)); err2 != nil {
			if err == nil {
				err = err2
			} else {
				dlog.Error(c, err2)
			}
		}
		if err != nil {
			dlog.Errorf(c, "Server exited with error %v", err)
		} else {
			dlog.Debug(c, "Server done")
		}
		// leave it locked
	}()

	dlog.Debug(c, "Enabling")
	err = o.translator.Enable(c)
	if err != nil {
		return err
	}
	o.tablesLock.Unlock()

	if o.overridePrimaryDNS {
		dlog.Debugf(c, "Bootstrapping local DNS server on port %d", o.dnsRedirPort)
		route, err := nat.NewRoute("udp", net.ParseIP(o.dnsIP), nil, o.dnsRedirPort)
		if err != nil {
			return err
		}
		err = o.doUpdate(c, nil, &nat.Table{
			Name:   "bootstrap",
			Routes: []*nat.Route{route},
		})
		if err != nil {
			dlog.Error(c, err)
		}
		dns.Flush()
	}

	dlog.Debug(c, "Starting server")
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
	return nil
}

func (o *outbound) resolveNoSearch(query string) []string {
	o.domainsLock.RLock()

	// Check if this is a NAME.NAMESPACE. query
	ds := strings.Split(query, ".")
	if len(ds) == 3 && ds[2] == "" {
		if _, ok := o.namespaces[ds[1]]; ok {
			query += "svc.cluster.local."
		}
	}
	ips := o.domains[strings.ToLower(query)]
	o.domainsLock.RUnlock()
	return shuffleIPs(ips)
}

// Since headless and externalName services can have multiple IPs,
// we return a shuffled list of the IPs if there are more than one.
func shuffleIPs(ips []string) []string {
	switch lenIPs := len(ips); lenIPs {
	case 0:
		return []string{}
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

func (o *outbound) destination(conn *net.TCPConn) (string, error) {
	return o.translator.GetOriginalDst(conn)
}

func (o *outbound) update(table *rpc.Table) (err error) {
	// Update stems from the connector so the destination target must be set on all routes
	routes := make([]*nat.Route, 0, len(table.Routes))
	domains := make(map[string][]string)
	for _, route := range table.Routes {
		ports, err := nat.ParsePorts(route.Port)
		if err != nil {
			return err
		}
		for _, ip := range route.Ips {
			r, err := nat.NewRoute(route.Proto, net.ParseIP(ip), ports, o.proxyRedirPort)
			if err != nil {
				return err
			}
			routes = append(routes, r)
		}
		if dn := route.Name; dn != "" {
			dn = strings.ToLower(dn) + "."
			domains[dn] = append(domains[dn], route.Ips...)
		}
	}
	o.work <- func(c context.Context) error {
		return o.doUpdate(c, domains, &nat.Table{
			Name:   table.Name,
			Routes: routes,
		})
	}
	return nil
}

func (o *outbound) noMoreUpdates() {
	close(o.work)
}

func (o *outbound) doUpdate(c context.Context, domains map[string][]string, table *nat.Table) error {
	// Make a copy of the current table
	o.tablesLock.Lock()
	defer o.tablesLock.Unlock()
	oldTable, ok := o.tables[table.Name]
	oldRoutes := make(map[nat.Destination]*nat.Route)
	if ok {
		for _, route := range oldTable.Routes {
			oldRoutes[route.Destination] = route
		}
	}

	// We're updating routes. Make sure DNS waits until the new answer
	// is ready, i.e. don't serve old answers.
	o.domainsLock.Lock()
	defer o.domainsLock.Unlock()
	o.domains = domains

	// Operate on the copy of the current table and the new table
	routesChanged := false
	for _, newRoute := range table.Routes {
		// Add the new version. This will clear out the old one if it exists
		changed, err := o.translator.Add(c, newRoute)
		if err != nil {
			dlog.Errorf(c, "forward %s: %v", newRoute, err)
		} else if changed {
			routesChanged = true
		}

		// remove the route from our map of old routes so we
		// don't end up deleting it below
		delete(oldRoutes, newRoute.Destination)
	}

	for _, route := range oldRoutes {
		changed, err := o.translator.Clear(c, route)
		if err != nil {
			dlog.Errorf(c, "clear %s: %v", route, err)
		} else if changed {
			routesChanged = true
		}
	}

	if routesChanged {
		if err := o.translator.Flush(c); err != nil {
			dlog.Errorf(c, "flush: %v", err)
		}
	}

	if table.Routes == nil || len(table.Routes) == 0 {
		delete(o.tables, table.Name)
	} else {
		o.tables[table.Name] = table
	}
	return nil
}

// SetSearchPath updates the DNS search path used by the resolver
func (o *outbound) setSearchPath(c context.Context, paths []string) {
	o.searchLock.Lock()
	defer o.searchLock.Unlock()
	o.setSearchPathFunc(c, paths)
}
