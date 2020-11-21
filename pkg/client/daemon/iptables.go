package daemon

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/pkg/client/daemon/nat"
	"github.com/datawire/telepresence2/pkg/client/daemon/proxy"
	rpc "github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/iptables"
)

type ipTables struct {
	dnsIP      string
	fallbackIP string
	noSearch   bool
	translator *nat.Translator
	tables     map[string]*iptables.Table
	tablesLock sync.RWMutex

	domains     map[string]*iptables.Route
	domainsLock sync.RWMutex

	search     []string
	searchLock sync.RWMutex

	work chan func(context.Context) error
}

func newIPTables(name string, dnsIP, fallbackIP string, noSearch bool) *ipTables {
	ret := &ipTables{
		dnsIP:      dnsIP,
		fallbackIP: fallbackIP,
		noSearch:   noSearch,
		tables:     make(map[string]*iptables.Table),
		translator: nat.NewTranslator(name),
		domains:    make(map[string]*iptables.Route),
		search:     []string{""},
		work:       make(chan func(context.Context) error),
	}
	ret.tablesLock.Lock() // leave it locked until translatorWorker unlocks it
	return ret
}

func (i *ipTables) dnsServerWorker(c context.Context) error {
	srv := dns.NewServer(c, dnsListeners(c, DNSRedirPort), i.fallbackIP+":53", func(domain string) string {
		if r := i.Resolve(domain); r != nil {
			return r.Ip
		}
		return ""
	})
	dgroup.ParentGroup(c).Go(ProxyWorker, i.proxyWorker)
	dlog.Debug(c, "Starting server")
	err := srv.Run(c)
	dlog.Debug(c, "Server done")
	return err
}

func (i *ipTables) proxyWorker(c context.Context) error {
	// hmm, we may not actually need to get the original
	// destination, we could just forward each ip to a unique port
	// and either listen on that port or run port-forward
	pr, err := proxy.NewProxy(c, ":"+ProxyRedirPort, i.destination)
	if err != nil {
		return errors.Wrap(err, "Proxy")
	}
	dgroup.ParentGroup(c).Go(TranslatorWorker, i.translatorWorker)
	dlog.Debug(c, "Starting server")
	pr.Run(c, 10000)
	dlog.Debug(c, "Server done")
	return nil
}

func (i *ipTables) dnsConfigWorker(c context.Context) error {
	dlog.Debug(c, "Starting server")
	bootstrap := iptables.Table{Name: "bootstrap", Routes: []*iptables.Route{{
		Ip:     i.dnsIP,
		Target: DNSRedirPort,
		Proto:  "udp",
	}}}
	i.update(&bootstrap)
	dns.Flush()

	if i.noSearch {
		<-c.Done()
	} else {
		restore := dns.OverrideSearchDomains(c, ".")
		<-c.Done()
		restore()
	}
	dns.Flush()
	dlog.Debug(c, "Server done")
	return nil
}

func (i *ipTables) translatorWorker(c context.Context) (err error) {
	defer func() {
		i.tablesLock.Lock()
		if err2 := i.translator.Disable(dcontext.HardContext(c)); err2 != nil {
			if err == nil {
				err = err2
			}
		}
		// leave it locked
	}()

	dlog.Debug(c, "Enabling")
	err = i.translator.Enable(c)
	if err != nil {
		return err
	}
	i.tablesLock.Unlock()

	dgroup.ParentGroup(c).Go(DNSConfigWorker, i.dnsConfigWorker)

	dlog.Debug(c, "Starting server")
	for {
		select {
		case <-c.Done():
			dlog.Debug(c, "Server done")
			return nil
		case f := <-i.work:
			if err = f(c); err != nil {
				dlog.Error(c, err.Error())
			}
		}
	}
}

// Resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (i *ipTables) Resolve(query string) *iptables.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
	}

	var route *iptables.Route
	i.searchLock.RLock()
	i.domainsLock.RLock()
	for _, suffix := range i.search {
		name := query + suffix
		if route = i.domains[strings.ToLower(name)]; route != nil {
			break
		}
	}
	i.searchLock.RUnlock()
	i.domainsLock.RUnlock()
	return route
}

func (i *ipTables) get(tableName string) *iptables.Table {
	i.tablesLock.RLock()
	table := i.tables[tableName]
	i.tablesLock.RUnlock()
	return table
}

func (i *ipTables) getAll() *rpc.Tables {
	tables := &rpc.Tables{}
	i.tablesLock.RLock()
	for _, t := range i.tables {
		tables.Tables = append(tables.Tables, t)
	}
	i.tablesLock.RUnlock()
	return tables
}

func (i *ipTables) destination(conn *net.TCPConn) (string, error) {
	_, host, err := i.translator.GetOriginalDst(conn)
	return host, err
}

func (i *ipTables) delete(table string) bool {
	result := make(chan bool)
	i.work <- func(c context.Context) error {
		i.tablesLock.Lock()
		defer i.tablesLock.Unlock()
		i.domainsLock.Lock()
		defer i.domainsLock.Unlock()

		var names []string
		if table == "" {
			for name := range i.tables {
				names = append(names, name)
			}
		} else if _, ok := i.tables[table]; ok {
			names = []string{table}
		} else {
			result <- false
			return nil
		}

		for _, name := range names {
			if name != "bootstrap" {
				err := i.doUpdate(c, &iptables.Table{Name: name})
				if err != nil {
					return err
				}
			}
		}

		result <- true
		return nil
	}
	return <-result
}

func (i *ipTables) update(table *iptables.Table) {
	result := make(chan error)
	i.work <- func(c context.Context) error {
		defer close(result)
		return i.doUpdate(c, table)
	}
	<-result
}

func routesEqual(a, b *iptables.Route) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Action == b.Action && a.Ip == b.Ip && a.Port == b.Port && a.Target == b.Target
}

func domain(r *iptables.Route) string {
	return strings.ToLower(r.Name + ".")
}

func (i *ipTables) doUpdate(c context.Context, table *iptables.Table) error {
	// Make a copy of the current table
	i.tablesLock.RLock()
	oldTable, ok := i.tables[table.Name]
	oldRoutes := make(map[string]*iptables.Route)
	if ok {
		for _, route := range oldTable.Routes {
			oldRoutes[route.Name] = route
		}
	}
	i.tablesLock.RUnlock()

	// Operate on the copy of the current table and the new table
	for _, newRoute := range table.Routes {
		oldRoute, oldRouteOk := oldRoutes[newRoute.Name]
		// A nil Route (when oldRouteOk != true) will compare
		// inequal to any valid new Route.
		if !routesEqual(newRoute, oldRoute) {
			// We're updating a route. Make sure DNS waits until the new answer
			// is ready, i.e. don't serve the old answer.
			i.domainsLock.Lock()

			// delete the old version
			if oldRouteOk {
				switch newRoute.Proto {
				case "tcp":
					i.translator.ClearTCP(c, oldRoute.Ip, oldRoute.Port)
				case "udp":
					i.translator.ClearUDP(c, oldRoute.Ip, oldRoute.Port)
				default:
					dlog.Warnf(c, "unrecognized protocol: %v", newRoute)
				}
			}
			// and add the new version
			if newRoute.Target != "" {
				switch newRoute.Proto {
				case "tcp":
					i.translator.ForwardTCP(c, newRoute.Ip, newRoute.Port, newRoute.Target)
				case "udp":
					i.translator.ForwardUDP(c, newRoute.Ip, newRoute.Port, newRoute.Target)
				default:
					dlog.Warnf(c, "unrecognized protocol: %v", newRoute)
				}
			}

			if newRoute.Name != "" {
				domain := domain(newRoute)
				dlog.Debugf(c, "STORE %v->%v", domain, newRoute)
				i.domains[domain] = newRoute
			}

			i.domainsLock.Unlock()
		}

		// remove the route from our map of old routes so we
		// don't end up deleting it below
		delete(oldRoutes, newRoute.Name)
	}

	// Clear out stale routes and DNS names
	i.domainsLock.Lock()
	for _, route := range oldRoutes {
		domain := domain(route)
		dlog.Debugf(c, "CLEAR %v->%v", domain, route)
		delete(i.domains, domain)

		switch route.Proto {
		case "tcp":
			i.translator.ClearTCP(c, route.Ip, route.Port)
		case "udp":
			i.translator.ClearUDP(c, route.Ip, route.Port)
		default:
			dlog.Warnf(c, "unrecognized protocol: %v", route)
		}
	}
	i.domainsLock.Unlock()

	// Update the externally-visible table
	i.tablesLock.Lock()
	if table.Routes == nil || len(table.Routes) == 0 {
		delete(i.tables, table.Name)
	} else {
		i.tables[table.Name] = table
	}
	i.tablesLock.Unlock()

	return nil
}

// SetSearchPath updates the DNS search path used by the resolver
func (i *ipTables) setSearchPath(paths []string) {
	i.searchLock.Lock()
	i.search = paths
	i.searchLock.Unlock()
}

// GetSearchPath retrieves the current search path
func (i *ipTables) searchPath() (sp []string) {
	i.searchLock.RLock()
	sp = i.search
	i.searchLock.RUnlock()
	return
}
