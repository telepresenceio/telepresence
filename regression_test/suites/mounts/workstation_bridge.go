package mounts

import (
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// WshardBridge proves that the port that --local-mount-port opens on the
// workstation accepts connections on the loopback address only.
type WshardBridge struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardBridge{},
		rt.InArea("mounts"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE),
		rt.NotOn("windows"),
	)
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

// wshardDial reports whether a TCP connection to host:port can be made.
func wshardDial(host string, port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Test_BridgePortIsLoopbackOnly intercepts with --local-mount-port and asserts
// that the port accepts connections on 127.0.0.1 and on no other address of
// the workstation.
func (s *WshardBridge) Test_BridgePortIsLoopbackOnly() {
	t := s.T()
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("sshfs"); err != nil {
			t.Skip("sshfs is not installed: the client disables mounts")
		}
	}
	others := wshardNonLoopbackAddrs()
	if len(others) == 0 {
		t.Skip("the workstation has no address besides the loopback addresses")
	}

	conn := s.Connect()
	wl := s.Workload(workloads.EchoWithConfigVolume("wshard-bridge"))
	ports, err := ioutil.FreePortsTCP(1)
	s.Require().NoError(err)
	port := int(ports[0].Port())

	localMountPort := cli.InterceptOpt(func() []string { return []string{"--local-mount-port", strconv.Itoa(port)} })
	a := conn.Intercept(t, wl, localMountPort)
	defer a.Detach(t)

	s.Eventually(func() bool { return wshardDial("127.0.0.1", port) }, mountTimeout, time.Second,
		"the mount bridge must accept connections on 127.0.0.1:%d", port)
	for _, addr := range others {
		s.False(wshardDial(addr, port), "the mount bridge port %d must not be reachable on %s", port, addr)
	}
}
