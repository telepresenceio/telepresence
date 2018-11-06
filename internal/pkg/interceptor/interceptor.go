package interceptor

import (
	"encoding/json"
	"log"
	"net"
	"strings"
	"sync"
	"github.com/datawire/teleproxy/internal/pkg/nat"
	rt "github.com/datawire/teleproxy/internal/pkg/route"
)

type Interceptor struct {
	port string
	tables map[string]rt.Table
	translator *nat.Translator
	domains sync.Map
	work chan func()
	done chan empty
}

type empty interface {}

func NewInterceptor(name string) *Interceptor {
	return &Interceptor{
		tables: make(map[string]rt.Table),
		translator: nat.NewTranslator(name),
		work: make(chan func()),
		done: make(chan empty),
	}
}

func (i *Interceptor) Start() {
	go func() {
		defer close(i.done)
		i.translator.Enable()
		defer i.translator.Disable()
		for {
			action, ok := <- i.work
			if ok {
				action()
			} else {
				return
			}
		}
	}()
}

func (i *Interceptor) Stop() {
	close(i.work)
	<- i.done
}

func (i *Interceptor) Resolve(name string) *rt.Route {
	value, ok := i.domains.Load(strings.ToLower(name))
	if ok {
		return value.(*rt.Route)
	} else {
		return nil
	}
}

func (i *Interceptor) Destination(conn *net.TCPConn) (string, error) {
	_, host, err := i.translator.GetOriginalDst(conn)
	return host, err
}

func (i *Interceptor) Render(table string) string {
	result := make(chan string, 1)
	i.work <- func() {
		var obj interface{}

		if table == "" {
			var tables []rt.Table
			for _, t := range i.tables {
				tables = append(tables, t)
			}
			obj = tables
		} else {
			var ok bool
			obj, ok = i.tables[table]
			if !ok {
				result <- ""
				return
			}
		}

		bytes, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			result <- err.Error()
		} else {
			result <- string(bytes)
		}
	}
	return <- result
}

func (i *Interceptor) Delete(table string) bool {
	result := make(chan bool, 1)
	i.work <- func() {
		var names []string
		if table == "" {
			for name := range i.tables {
				names = append(names, name)
			}
		} else if _, ok := i.tables[table]; ok {
			names = []string{table}
		} else {
			result <- false
		}

		for _, name := range names {
			if name != "bootstrap" {
				i.update(rt.Table{Name: name})
			}
		}

		result <- true
	}
	return <- result
}

func (i *Interceptor) Update(table rt.Table) {
	i.work <- func() {
		i.update(table)
	}
}

func (i *Interceptor) update(table rt.Table) {
	old, ok := i.tables[table.Name]

	routes := make(map[string]rt.Route)
	if ok {
		for _, route := range old.Routes {
			routes[route.Name] = route
		}
	}

	for _, route := range table.Routes {
		existing, ok := routes[route.Name]
		if ok && route != existing {

			switch route.Proto {
			case "tcp":
				i.translator.ClearTCP(existing.Ip)
			case "udp":
				i.translator.ClearUDP(existing.Ip)
			default:
				log.Printf("unrecognized protocol: %v", route)
			}

		}

		if !ok || route != existing {

			if route.Target != "" {
				switch route.Proto {
				case "tcp":
					i.translator.ForwardTCP(route.Ip, route.Target)
				case "udp":
					i.translator.ForwardUDP(route.Ip, route.Target)
				default:
					log.Printf("unrecognized protocol: %v", route)
				}
			}

			if route.Name != "" {
				log.Printf("Storing %v->%v", route.Domain(), route)
				copy := route
				i.domains.Store(route.Domain(), &copy)
			}

		}

		if ok {
			// remove the route from our map of
			// old routes so we don't end up
			// deleting it below
			delete(routes, route.Name)
		}
	}

	for _, route := range routes {
		log.Printf("Clearing %v->%v", route.Domain(), route)
		i.domains.Delete(route.Domain())

		switch route.Proto {
		case "tcp":
			i.translator.ClearTCP(route.Ip)
		case "udp":
			i.translator.ClearUDP(route.Ip)
		default:
			log.Printf("unrecognized protocol: %v", route)
		}

	}

	if table.Routes == nil || len(table.Routes) == 0 {
		delete(i.tables, table.Name)
	} else {
		i.tables[table.Name] = table
	}
}
