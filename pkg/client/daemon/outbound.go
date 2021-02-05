package daemon

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/nat"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/proxy"
	"github.com/datawire/telepresence2/v2/pkg/subnet"
)

const (
	// proxyRedirPort is the port to which we redirect proxied IPs. It
	// should probably eventually be configurable and/or dynamically
	// chosen.
	proxyRedirPort = "1234"
)

// outbound does stuff, idk, I didn't write it.
//
// A zero outbound is invalid; you must use newOutbound.
type outbound struct {
	localIP    net.IP
	dnsIP      string
	fallbackIP string
	noSearch   bool
	translator nat.FirewallRouter
	tables     map[string]*rpc.Table
	tablesLock sync.RWMutex

	domains           map[string]*rpc.Route
	domainsLock       sync.RWMutex
	setSearchPathFunc func(c context.Context, paths []string)

	search     []string
	searchLock sync.RWMutex

	overridePrimaryDNS bool

	// dnsRedirPort is the port to which we redirect dns requests.
	dnsRedirPort int

	work chan func(context.Context) error
}

// newOutbound returns a new properly initialized outbound object.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func newOutbound(name string, dnsIP, fallbackIP string, noSearch bool) (*outbound, error) {
	var localIP net.IP
	if runtime.GOOS == "darwin" {
		// TODO(lukeshu): Unify the GNU/Linux code with this.
		loopBackCIDR, err := subnet.FindAvailableLoopBackClassC()
		if err != nil {
			return nil, err
		}
		// Place the daemon DNS & firewall-to-socks servers in the private network at
		// x.x.x.2.
		localIP = make(net.IP, len(loopBackCIDR.IP))
		copy(localIP, loopBackCIDR.IP)
		localIP[len(localIP)-1] = 2
	}

	ret := &outbound{
		localIP:    localIP,
		dnsIP:      dnsIP,
		fallbackIP: fallbackIP,
		noSearch:   noSearch,
		tables:     make(map[string]*rpc.Table),
		translator: nat.NewRouter(name, localIP),
		domains:    make(map[string]*rpc.Route),
		search:     []string{""},
		work:       make(chan func(context.Context) error),
	}
	ret.tablesLock.Lock() // leave it locked until firewallConfiguratorWorker unlocks it
	return ret, nil
}

// firewall2socksWorker listens on localhost:1234 and forwards those connections to the connector's
// SOCKS server, for them to be forwarded to the cluster.  We count on firewallConfiguratorWorker
// having configured the host firewall to send all cluster-bound TCP connections to this port, and
// we make special syscalls/ioctls to determine where each connection was originally bound for, so
// that we know what to tell SOCKS.
func (o *outbound) firewall2socksWorker(c context.Context, onReady func()) error {
	// hmm, we may not actually need to get the original
	// destination, we could just forward each ip to a unique port
	// and either listen on that port or run port-forward
	pr, err := proxy.NewProxy(c, ":"+proxyRedirPort, o.destination)
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
		err := o.doUpdate(c, &rpc.Table{
			Name: "bootstrap",
			Routes: []*rpc.Route{
				{
					Ip:     o.dnsIP,
					Target: strconv.Itoa(o.dnsRedirPort),
					Proto:  "udp",
				},
			},
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

func (o *outbound) resolveNoNS(query string) *rpc.Route {
	o.domainsLock.RLock()
	route := o.domains[strings.ToLower(query)]
	o.domainsLock.RUnlock()
	return route
}

func (o *outbound) destination(conn *net.TCPConn) (string, error) {
	_, host, err := o.translator.GetOriginalDst(conn)
	return host, err
}

func (o *outbound) update(table *rpc.Table) {
	o.work <- func(c context.Context) error {
		return o.doUpdate(c, table)
	}
}

func (o *outbound) noMoreUpdates() {
	close(o.work)
}

func routesEqual(a, b *rpc.Route) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Action == b.Action && a.Ip == b.Ip && a.Port == b.Port && a.Target == b.Target
}

func domain(r *rpc.Route) string {
	return strings.ToLower(r.Name + ".")
}

func (o *outbound) doUpdate(c context.Context, table *rpc.Table) error {
	// Make a copy of the current table
	o.tablesLock.RLock()
	oldTable, ok := o.tables[table.Name]
	oldRoutes := make(map[string]*rpc.Route)
	if ok {
		for _, route := range oldTable.Routes {
			oldRoutes[route.Name] = route
		}
	}
	o.tablesLock.RUnlock()

	// Operate on the copy of the current table and the new table
	for _, newRoute := range table.Routes {
		oldRoute, oldRouteOk := oldRoutes[newRoute.Name]
		// A nil Route (when oldRouteOk != true) will compare
		// inequal to any valid new Route.
		if !routesEqual(newRoute, oldRoute) {
			// We're updating a route. Make sure DNS waits until the new answer
			// is ready, i.e. don't serve the old answer.
			o.domainsLock.Lock()

			// delete the old version
			if oldRouteOk {
				switch newRoute.Proto {
				case "tcp":
					if err := o.translator.ClearTCP(c, oldRoute.Ip, oldRoute.Port); err != nil {
						dlog.Errorf(c, "clear tpc: %v", err)
					}
				case "udp":
					if err := o.translator.ClearUDP(c, oldRoute.Ip, oldRoute.Port); err != nil {
						dlog.Errorf(c, "clear udp: %v", err)
					}
				default:
					dlog.Warnf(c, "unrecognized protocol: %v", newRoute)
				}
			}
			// and add the new version
			if newRoute.Target != "" {
				switch newRoute.Proto {
				case "tcp":
					if err := o.translator.ForwardTCP(c, newRoute.Ip, newRoute.Port, newRoute.Target); err != nil {
						dlog.Errorf(c, "forward tcp: %v", err)
					}
				case "udp":
					if err := o.translator.ForwardUDP(c, newRoute.Ip, newRoute.Port, newRoute.Target); err != nil {
						dlog.Errorf(c, "forward udp: %v", err)
					}
				default:
					dlog.Warnf(c, "unrecognized protocol: %v", newRoute)
				}
			}

			if newRoute.Name != "" {
				domain := domain(newRoute)
				dlog.Debugf(c, "STORE %v->%v", domain, newRoute)
				o.domains[domain] = newRoute
			}

			o.domainsLock.Unlock()
		}

		// remove the route from our map of old routes so we
		// don't end up deleting it below
		delete(oldRoutes, newRoute.Name)
	}

	// Clear out stale routes and DNS names
	o.domainsLock.Lock()
	for _, route := range oldRoutes {
		domain := domain(route)
		dlog.Debugf(c, "CLEAR %v->%v", domain, route)
		delete(o.domains, domain)

		switch route.Proto {
		case "tcp":
			if err := o.translator.ClearTCP(c, route.Ip, route.Port); err != nil {
				dlog.Errorf(c, "clear tpc: %v", err)
			}
		case "udp":
			if err := o.translator.ClearUDP(c, route.Ip, route.Port); err != nil {
				dlog.Errorf(c, "clear udp: %v", err)
			}
		default:
			dlog.Warnf(c, "unrecognized protocol: %v", route)
		}
	}
	o.domainsLock.Unlock()

	// Update the externally-visible table
	o.tablesLock.Lock()
	if table.Routes == nil || len(table.Routes) == 0 {
		delete(o.tables, table.Name)
	} else {
		o.tables[table.Name] = table
	}
	o.tablesLock.Unlock()

	return nil
}

// SetSearchPath updates the DNS search path used by the resolver
func (o *outbound) setSearchPath(c context.Context, paths []string) {
	o.searchLock.Lock()
	defer o.searchLock.Unlock()
	o.setSearchPathFunc(c, paths)
}
