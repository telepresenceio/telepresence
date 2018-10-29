package interceptor

import (
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

func (i *Interceptor) Update(table rt.Table) {
	i.work <- func() {
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
					panic("unrecognized protocol")
				}

			}

			if !ok || route != existing {

				switch route.Proto {
				case "tcp":
					i.translator.ForwardTCP(route.Ip, route.Target)
				case "udp":
					i.translator.ForwardUDP(route.Ip, route.Target)
				default:
					panic("unrecognized protocol")
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
			i.domains.Delete(route.Domain())

			switch route.Proto {
			case "tcp":
				i.translator.ClearTCP(route.Ip)
			case "udp":
				i.translator.ClearUDP(route.Ip)
			default:
				panic("unrecognized protocol")
			}

		}

		i.tables[table.Name] = table
	}
}
