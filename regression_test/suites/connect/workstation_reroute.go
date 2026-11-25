package connect

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// WshardReroute proves that the local port that --reroute-local opens accepts
// connections on the loopback address only.
type WshardReroute struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardReroute{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// wshardNonLoopbackAddrs returns the IPv4 addresses of the workstation that
// are not loopback addresses.
func wshardNonLoopbackAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
			out = append(out, ip4.String())
		}
	}
	return out
}

// Test_ReroutedPortIsLoopbackOnly connects with --reroute-local to a service of
// the cluster, asserts that the service answers on 127.0.0.1 and that the same
// port is closed on every other address of the workstation.
func (s *WshardReroute) Test_ReroutedPortIsLoopbackOnly() {
	t := s.T()
	ns := s.AppNamespace()
	others := wshardNonLoopbackAddrs()
	if len(others) == 0 {
		t.Skip("the workstation has no address besides the loopback addresses")
	}

	s.Manager()
	wl := s.Workload(workloads.Echo("wshard-reroute"))
	ports, err := ioutil.FreePortsTCP(1)
	s.Require().NoError(err)
	port := int(ports[0].Port())

	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnExtraArgs("--reroute-local",
		fmt.Sprintf("%d:%s.%s:%d", port, wl.SvcName, wl.Namespace, wl.Port)))
	t.Cleanup(func() { conn.Disconnect(t) })

	check.EventuallyHTTP(t, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		func(status int, _ string) bool { return status == 200 }, 30*time.Second)
	for _, addr := range others {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(port)), 3*time.Second)
		if err == nil {
			_ = c.Close()
		}
		s.Error(err, "the rerouted port %d must not be reachable on %s", port, addr)
	}
}
