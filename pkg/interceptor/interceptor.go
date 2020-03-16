package interceptor

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/datawire/ambassador/pkg/supervisor"

	"github.com/datawire/ambassador/internal/pkg/nat"
	rt "github.com/datawire/ambassador/internal/pkg/route"
)

type Interceptor struct {
	translator *nat.Translator
	tables     map[string]rt.Table
	tablesLock sync.RWMutex

	domains     map[string]rt.Route
	domainsLock sync.RWMutex

	search     []string
	searchLock sync.RWMutex

	work chan func(*supervisor.Process) error
}

func NewInterceptor(name string) *Interceptor {
	ret := &Interceptor{
		tables:     make(map[string]rt.Table),
		translator: nat.NewTranslator(name),
		domains:    make(map[string]rt.Route),
		search:     []string{""},
		work:       make(chan func(*supervisor.Process) error),
	}
	ret.tablesLock.Lock() // leave it locked until .Start() unlocks it
	return ret
}

func (i *Interceptor) Work(p *supervisor.Process) error {
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

// Resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (i *Interceptor) Resolve(query string) *rt.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
	}

	const prefix = "teleproxy"
	const suffix = ".cachebust.telepresence.io." // must end with .
	const replacement = "teleproxy."             // must end with .
	if strings.HasPrefix(query, prefix) && strings.HasSuffix(query, suffix) {
		query = replacement
	}

	i.searchLock.RLock()
	defer i.searchLock.RUnlock()
	i.domainsLock.RLock()
	defer i.domainsLock.RUnlock()

	for _, suffix := range i.search {
		name := query + suffix
		value, ok := i.domains[strings.ToLower(name)]
		if ok {
			return &value
		}
	}
	return nil
}

func (i *Interceptor) Destination(conn *net.TCPConn) (string, error) {
	_, host, err := i.translator.GetOriginalDst(conn)
	return host, err
}

func (i *Interceptor) Render(table string) string {
	var obj interface{}

	if table == "" {
		var tables []rt.Table
		i.tablesLock.RLock()
		for _, t := range i.tables {
			tables = append(tables, t)
		}
		i.tablesLock.RUnlock()
		obj = tables
	} else {
		var ok bool
		i.tablesLock.RLock()
		obj, ok = i.tables[table]
		i.tablesLock.RUnlock()
		if !ok {
			return ""
		}
	}

	bytes, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err.Error()
	} else {
		return string(bytes)
	}
}

func (i *Interceptor) Delete(table string) bool {
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
				err := i.update(p, rt.Table{Name: name})
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

func (i *Interceptor) Update(table rt.Table) {
	result := make(chan struct{})
	i.work <- func(p *supervisor.Process) error {
		defer close(result)
		return i.update(p, table)
	}
	<-result
}

func (i *Interceptor) update(p *supervisor.Process, table rt.Table) error {
	// Make a copy of the current table
	i.tablesLock.Lock()
	oldTable, ok := i.tables[table.Name]
	oldRoutes := make(map[string]rt.Route)
	if ok {
		for _, route := range oldTable.Routes {
			oldRoutes[route.Name] = route
		}
	}
	i.tablesLock.Unlock()

	// Operate on the copy of the current table and the new table
	for _, newRoute := range table.Routes {
		oldRoute, oldRouteOk := oldRoutes[newRoute.Name]
		// A nil Route (when oldRouteOk != true) will compare
		// inequal to any valid new Route.
		if newRoute != oldRoute {
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
				log.Printf("INT: STORE %v->%v", newRoute.Domain(), newRoute)
				i.domains[newRoute.Domain()] = newRoute
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
		log.Printf("INT: CLEAR %v->%v", route.Domain(), route)
		delete(i.domains, route.Domain())

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
func (i *Interceptor) SetSearchPath(paths []string) {
	i.searchLock.Lock()
	defer i.searchLock.Unlock()

	i.search = paths
}

// GetSearchPath retrieves the current search path
func (i *Interceptor) GetSearchPath() []string {
	i.searchLock.RLock()
	defer i.searchLock.RUnlock()

	return i.search
}
