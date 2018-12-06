package interceptor

import (
	"encoding/json"
	"github.com/datawire/teleproxy/internal/pkg/nat"
	rt "github.com/datawire/teleproxy/internal/pkg/route"
	"log"
	"net"
	"strings"
	"sync"
)

type Interceptor struct {
	translator *nat.Translator
	tables     map[string]rt.Table
	tablesLock sync.RWMutex

	domains     map[string]rt.Route
	domainsLock sync.RWMutex

	search     []string
	searchLock sync.RWMutex
}

func NewInterceptor(name string) *Interceptor {
	ret := &Interceptor{
		tables:     make(map[string]rt.Table),
		translator: nat.NewTranslator(name),
		domains:    make(map[string]rt.Route),
		search:     []string{""},
	}
	ret.tablesLock.Lock() // leave it locked until .Start() unlocks it
	return ret
}

func (i *Interceptor) Start() {
	go func() {
		i.translator.Enable()
		i.tablesLock.Unlock()
	}()
}

func (i *Interceptor) Stop() {
	i.tablesLock.Lock()
	i.translator.Disable()
	// leave it locked
}

// Resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (i *Interceptor) Resolve(query string) *rt.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
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
		return false
	}

	for _, name := range names {
		if name != "bootstrap" {
			i.update(rt.Table{Name: name})
		}
	}

	return true
}

func (i *Interceptor) Update(table rt.Table) {
	i.tablesLock.Lock()
	defer i.tablesLock.Unlock()
	i.domainsLock.Lock()
	defer i.domainsLock.Unlock()

	i.update(table)
}

// .update() assumes that both .tablesLock and .domainsLock are held
// for writing.  Ensuring that is the case is the caller's
// responsibility.
func (i *Interceptor) update(table rt.Table) {
	oldTable, ok := i.tables[table.Name]

	oldRoutes := make(map[string]rt.Route)
	if ok {
		for _, route := range oldTable.Routes {
			oldRoutes[route.Name] = route
		}
	}

	for _, newRoute := range table.Routes {
		oldRoute, oldRouteOk := oldRoutes[newRoute.Name]
		// A nil Route (when oldRouteOk != true) will compare
		// inequal to any valid new Route.
		if newRoute != oldRoute {
			// delete the old version
			if oldRouteOk {
				switch newRoute.Proto {
				case "tcp":
					i.translator.ClearTCP(oldRoute.Ip)
				case "udp":
					i.translator.ClearUDP(oldRoute.Ip)
				default:
					log.Printf("INT: unrecognized protocol: %v", newRoute)
				}
			}
			// and add the new version
			if newRoute.Target != "" {
				switch newRoute.Proto {
				case "tcp":
					i.translator.ForwardTCP(newRoute.Ip, newRoute.Target)
				case "udp":
					i.translator.ForwardUDP(newRoute.Ip, newRoute.Target)
				default:
					log.Printf("INT: unrecognized protocol: %v", newRoute)
				}
			}

			if newRoute.Name != "" {
				log.Printf("INT: STORE %v->%v", newRoute.Domain(), newRoute)
				i.domains[newRoute.Domain()] = newRoute
			}
		}

		// remove the route from our map of old routes so we
		// don't end up deleting it below
		delete(oldRoutes, newRoute.Name)
	}

	for _, route := range oldRoutes {
		log.Printf("INT: CLEAR %v->%v", route.Domain(), route)
		delete(i.domains, route.Domain())

		switch route.Proto {
		case "tcp":
			i.translator.ClearTCP(route.Ip)
		case "udp":
			i.translator.ClearUDP(route.Ip)
		default:
			log.Printf("INT: unrecognized protocol: %v", route)
		}

	}

	if table.Routes == nil || len(table.Routes) == 0 {
		delete(i.tables, table.Name)
	} else {
		i.tables[table.Name] = table
	}
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
