package nat

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

type env struct {
	name   string
	pfconf string
	before string
}

/*
 *  192.0.2.0/24 (TEST-NET-1),
 *  198.51.100.0/24 (TEST-NET-2)
 *  203.0.113.0/24 (TEST-NET-3)
 */

var environments = []env{
	{name: "empty", pfconf: ""},
	{name: "skip on lo", pfconf: "set skip on lo\n"},
	{name: "block return", pfconf: "block return quick proto tcp from any to 192.0.2.0/24\n"},
	{name: "anchor block return", pfconf: "anchor {\n    block return quick proto tcp from any to 192.0.2.0/24\n}\n"},
}

func (e *env) testName() string {
	return e.name
}
func (e *env) setup(c context.Context) error {
	o1, err := pfo(c, "-sr")
	if err != nil {
		return err
	}
	o2, err := pfo(c, "-sn")
	if err != nil {
		return err
	}
	output := string(o1) + string(o2)
	lines := strings.Split(output, "\n")

	for _, kw := range []string{"scrub-anchor", "nat-anchor", "rdr-anchor", "anchor"} {
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == kw {
				e.before += line + "\n"
			}
		}
	}

	err = pf(c, []string{"-F", "all"}, "")
	if err != nil {
		return err
	}
	return pf(c, []string{"-f-"}, e.pfconf)
}

func (e *env) teardown(c context.Context) error {
	_ = pf(c, []string{"-F", "all"}, "")
	return pf(c, []string{"-f-"}, e.before)
}

func makeRoute(t *testing.T, proto string, ip net.IP, ports []int, toPort int) *Route {
	route, err := NewRoute(proto, ip, ports, toPort)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return route
}

func TestSorted(t *testing.T) {
	routes := []*Route{
		makeRoute(t, "tcp", net.IPv4(192, 0, 2, 1), []int{80}, 4321),
		makeRoute(t, "tcp", net.IPv4(192, 0, 2, 1), []int{8080}, 12345),
		makeRoute(t, "tcp", net.IPv4(192, 0, 2, 2), nil, 15022),
		makeRoute(t, "tcp", net.IPv4(192, 0, 2, 11), nil, 4323),
		makeRoute(t, "udp", net.IPv4(192, 0, 2, 1), nil, 2134),
	}

	c := dlog.NewTestContext(t, false)
	g := dgroup.NewGroup(c, dgroup.GroupConfig{DisableLogging: true})
	g.Go("sorted-test", func(c context.Context) (err error) {
		tr := newRouter("test-table", net.IP{127, 0, 1, 2})
		if err = tr.Enable(c); err != nil {
			return err
		}
		defer func() {
			_ = tr.Disable(c)
		}()
		// Forward routes in reverse order
		for i := len(routes) - 1; i >= 0; i-- {
			if _, err = tr.Add(c, routes[i]); err != nil {
				return err
			}
		}
		entries := tr.sorted()
		if !reflect.DeepEqual(entries, routes) {
			return fmt.Errorf("not sorted: %s", entries)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
