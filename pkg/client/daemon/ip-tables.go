package daemon

import (
	"log"
	"strings"
	"sync"

	"github.com/datawire/ambassador/pkg/supervisor"

	"github.com/datawire/telepresence2/pkg/client/outbound/nat"
	rpc "github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/iptables"
)

type ipTables struct {
	translator *nat.Translator
	tables     map[string]*iptables.Table
	tablesLock sync.RWMutex

	domains     map[string]*iptables.Route
	domainsLock sync.RWMutex

	search     []string
	searchLock sync.RWMutex

	work chan func(*supervisor.Process) error
}

func newIpTables(name string) *ipTables {
	ret := &ipTables{
		tables:     make(map[string]*iptables.Table),
		translator: nat.NewTranslator(name),
		domains:    make(map[string]*iptables.Route),
		search:     []string{""},
		work:       make(chan func(*supervisor.Process) error),
	}
	ret.tablesLock.Lock() // leave it locked until .Start() unlocks it
	return ret
}

func (i *ipTables) Run(p *supervisor.Process) error {
	i.translator.Enable(p)
	i.tablesLock.Unlock()

	p.Ready()

	for {
		select {
		case <-p.Shutdown():
			i.tablesLock.Lock()
			i.translator.Disable(p)
			// leave it locked
			return nil
		case f := <-i.work:
			err := f(p)
			if err != nil {
				return err
			}
		}
	}
}

// resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (i *ipTables) Resolve(query string) *iptables.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
	}

	const prefix = "teleproxy"
	const suffix = ".cachebust.telepresence.io." // must end with .
	const replacement = "teleproxy."             // must end with .
	if strings.HasPrefix(query, prefix) && strings.HasSuffix(query, suffix) {
		query = replacement
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

func (i *ipTables) delete(table string) bool {
	result := make(chan bool)
	i.work <- func(p *supervisor.Process) error {
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
				err := i.doUpdate(p, &iptables.Table{Name: name})
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
	result := make(chan struct{})
	i.work <- func(p *supervisor.Process) error {
		defer close(result)
		return i.doUpdate(p, table)
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

func (i *ipTables) doUpdate(p *supervisor.Process, table *iptables.Table) error {
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
					i.translator.ClearTCP(p, oldRoute.Ip, oldRoute.Port)
				case "udp":
					i.translator.ClearUDP(p, oldRoute.Ip, oldRoute.Port)
				default:
					log.Printf("INT: unrecognized protocol: %v", newRoute)
				}
			}
			// and add the new version
			if newRoute.Target != "" {
				switch newRoute.Proto {
				case "tcp":
					i.translator.ForwardTCP(p, newRoute.Ip, newRoute.Port, newRoute.Target)
				case "udp":
					i.translator.ForwardUDP(p, newRoute.Ip, newRoute.Port, newRoute.Target)
				default:
					log.Printf("INT: unrecognized protocol: %v", newRoute)
				}
			}

			if newRoute.Name != "" {
				domain := domain(newRoute)
				log.Printf("INT: STORE %v->%v", domain, newRoute)
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
		log.Printf("INT: CLEAR %v->%v", domain, route)
		delete(i.domains, domain)

		switch route.Proto {
		case "tcp":
			i.translator.ClearTCP(p, route.Ip, route.Port)
		case "udp":
			i.translator.ClearUDP(p, route.Ip, route.Port)
		default:
			log.Printf("INT: unrecognized protocol: %v", route)
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
