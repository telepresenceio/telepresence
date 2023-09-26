package vif_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type RoutingSuite struct {
	suite.Suite
}

func TestRouting(t *testing.T) {
	suite.Run(t, new(RoutingSuite))
}

func getCidr(byte3, byte4 byte, mask int) *net.IPNet {
	// 198.18.0.0/15 is reserved for benchmarking.
	ip := net.IPv4(198, 18, byte3, byte4)
	return &net.IPNet{
		IP:   ip.To4(),
		Mask: net.CIDRMask(mask, 32),
	}
}

func (s *RoutingSuite) SetupSuite() {
	// Compile the router binary
	if runtime.GOOS == "windows" {
		// Run "make wintun.dll" in the ../../ directory
		err := dexec.CommandContext(context.Background(), "make", "-C", "../../", "build-output/bin/wintun.dll").Run()
		s.Require().NoError(err)
		// That'll place the DLL in ../../build-output/bin/wintun.dll so copy it to testdata/router
		err = dexec.CommandContext(context.Background(), "cp", "../../build-output/bin/wintun.dll", "testdata/router/wintun.dll").Run()
		s.Require().NoError(err)
		err = dexec.CommandContext(context.Background(), "go", "build", "-o", "testdata\\router\\router.exe", "testdata\\router\\main.go").Run()
		s.Require().NoError(err)
	} else {
		err := dexec.CommandContext(context.Background(), "go", "build", "-o", "testdata/router/router", "testdata/router/main.go").Run()
		s.Require().NoError(err)

		// Run sudo to get a password prompt out of the way
		err = proc.CacheAdmin(context.Background(), "")
		s.Require().NoError(err)
	}
	// Make sure there's no existing route
	cidr := getCidr(2, 1, 32)
	route, err := routing.GetRoute(context.Background(), cidr)
	s.Require().NoError(err)
	s.Require().True(route.Default || subnet.IsHalfOfDefault(route.RoutedNet), "There should be no route for %s, or everything will fail. Route is: %s", cidr.IP, route)
}

// The routes are all gonna be inside 100.64.0.0/10 which is assigned as a reserved block for NAT. Github machines map 10/8 sometimes, so we wanna make sure not to conflict

func (s *RoutingSuite) Test_RouteIsAdded() {
	ctx := context.Background()
	cidr := getCidr(2, 0, 24)

	ipnet := getCidr(2, 1, 32)

	device, routerCancel, err := s.runRouter(ctx, cidr.String())
	s.Require().NoError(err)
	defer routerCancel()

	route, err := routing.GetRoute(ctx, ipnet)
	s.Require().NoError(err)
	// Ensure that the route is for the right device
	s.Require().Equal(device, route.Interface.Name)
}

func (s *RoutingSuite) Test_RouteIsRemoved() {
	ctx := context.Background()
	cidr := getCidr(2, 0, 24)
	device, routerCancel, err := s.runRouter(ctx, cidr.String())
	s.Require().NoError(err)

	routerCancel()

	ipnet := getCidr(2, 1, 32)
	route, err := routing.GetRoute(ctx, ipnet)
	s.Require().NoError(err)

	s.Require().NotEqual(device, route.Interface.Name)
}

func (s *RoutingSuite) Test_RouteIsBlackListed() {
	ctx := context.Background()
	cidrYes := getCidr(2, 0, 24)
	cidrNo := getCidr(2, 4, 32)
	oldRoute, err := routing.GetRoute(ctx, cidrNo)
	s.Require().NoError(err)

	device, routerCancel, err := s.runRouter(ctx, cidrYes.String(), "!"+cidrNo.String())
	s.Require().NoError(err)
	defer routerCancel()

	route, err := routing.GetRoute(ctx, cidrNo)
	s.Require().NoError(err)

	s.Require().Equal(oldRoute.Interface.Name, route.Interface.Name, "Expected route %s got %s", oldRoute, route)
	s.Require().NotEqual(device, route.Interface.Name)
}

func (s *RoutingSuite) Test_RoutingTable() {
	ctx := context.Background()
	cidr := getCidr(2, 0, 24)

	device, routerCancel, err := s.runRouter(ctx, cidr.String())
	s.Require().NoError(err)
	defer routerCancel()

	routes, err := routing.GetRoutingTable(ctx)
	s.Require().NoError(err)
	deviceFound := false
	cidrFound := false
	for _, route := range routes {
		if route.Interface.Name == device {
			deviceFound = true
			s.Require().False(route.Default, fmt.Sprintf("Route %s is default", route.String()))
			s.Require().False(subnet.IsZeroMask(route.RoutedNet), fmt.Sprintf("Route %s has zero mask", route.String()))
			// Linux and Windows will automatically add a bunch of multicast routes, which we can ignore as they're not actually for routing through the device.
			if !route.RoutedNet.IP.IsMulticast() {
				if route.RoutedNet.IP.To4() == nil {
					s.Require().Contains([]net.IPMask{net.CIDRMask(128, 128), net.CIDRMask(64, 128)}, route.RoutedNet.Mask, fmt.Sprintf("Route %s is not a /128 or /64 mask", route.String()))
				} else {
					// 255.255.255.255/32 is a special broadcast address that won't actually be used for routing
					if !route.RoutedNet.IP.Equal(net.IPv4(255, 255, 255, 255)) {
						s.Require().True(cidr.Contains(route.RoutedNet.IP), fmt.Sprintf("Route %s is not contained in %s", route.String(), cidr))
					} else {
						s.Require().Equal(net.CIDRMask(32, 32), route.RoutedNet.Mask, fmt.Sprintf("Route %s is not a /32 mask", route.String()))
					}
				}
			}
			if route.RoutedNet.String() == cidr.String() {
				cidrFound = true
			}
		}
	}
	s.Require().True(deviceFound)
	s.Require().True(cidrFound)
}

func (s *RoutingSuite) Test_ConflictingRoutes() {
	// Start two routers with conflicting routes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cidr1 := getCidr(2, 0, 26)
	cidr2 := getCidr(2, 32, 27)

	_, routerCancel1, err := s.runRouter(ctx, cidr1.String())
	s.Require().NoError(err)
	defer routerCancel1()

	_, routerCancel2, err := s.runRouter(ctx, cidr2.String())
	if routerCancel2 != nil {
		// Make sure the second router doesn't leak
		defer routerCancel2()
	}
	s.Require().Error(err)
}

func (s *RoutingSuite) Test_WhitelistedRoutes() {
	ctx := context.Background()
	original := getCidr(0, 0, 16)
	conflicting := getCidr(4, 0, 24)
	whitelist := getCidr(0, 0, 21)

	device1, routerCancel, err := s.runRouter(ctx, original.String())
	s.Require().NoError(err)
	defer routerCancel()

	device2, routerCancel2, err := s.runRouter(ctx, conflicting.String(), "+"+whitelist.String())
	s.Require().NoError(err)
	defer routerCancel2()

	s.Require().NotEqual(device1, device2)

	ipnet := getCidr(4, 2, 32)

	route, err := routing.GetRoute(ctx, ipnet)
	s.Require().NoError(err)
	// Ensure that the route is for the right device
	s.Require().Equal(device2, route.Interface.Name, "Route %s is not for device %s", route, device2)
}

func (s *RoutingSuite) Test_VPNConflicts() {
	ctx := context.Background()
	cidr, ok := dos.LookupEnv(ctx, "VPN_CIDR")
	if !ok {
		s.T().Skip("VPN_CIDR not set, skipping test")
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	s.Require().NoError(err)
	ones, _ := ipnet.Mask.Size()
	s.Require().LessOrEqual(ones, 28, "VPN_CIDR mask is too small")
	ip := ipnet.IP.To4()
	s.Require().Equal(uint8(0x0), ip[3], "VPN_CIDR must begin at 0")
	ip[3] = 8
	conflicting := fmt.Sprintf("%s/29", ip.String())

	_, routerCancel, err := s.runRouter(ctx, conflicting)
	if err == nil {
		defer routerCancel()
	}
	s.Require().Error(err)
}

func (s *RoutingSuite) Test_VPNConflictsWithWhitelist() {
	ctx := context.Background()
	cidr, ok := dos.LookupEnv(ctx, "VPN_CIDR")
	if !ok {
		s.T().Skip("VPN_CIDR not set, skipping test")
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	s.Require().NoError(err)
	ones, _ := ipnet.Mask.Size()
	s.Require().LessOrEqual(ones, 28, "VPN_CIDR mask is too small")
	ip := ipnet.IP.To4()
	s.Require().Equal(uint8(0x0), ip[3], "VPN_CIDR must begin at 0")
	ip[3] = 8
	conflicting := fmt.Sprintf("%s/29", ip.String())

	device, routerCancel, err := s.runRouter(ctx, conflicting, "+"+cidr)
	s.Require().NoError(err)
	defer routerCancel()

	ip[3] += 1
	route, err := routing.GetRoute(ctx, &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)})
	s.Require().NoError(err)
	s.Require().Equal(device, route.Interface.Name)
}

func (s *RoutingSuite) Test_GetRoute() {
	ctx := context.Background()
	cidr := getCidr(2, 0, 24)

	ipnet := getCidr(2, 1, 32)

	device, routerCancel, err := s.runRouter(ctx, cidr.String())
	s.Require().NoError(err)
	defer routerCancel()

	route, err := routing.GetRoute(ctx, ipnet)

	// We know what this route is supposed to look like cause we just added it. Make sure it matches.
	s.Require().NoError(err)
	s.Require().Equal(device, route.Interface.Name)
	s.Require().Equal(cidr, route.RoutedNet)
	s.Require().False(route.Default)
	s.Require().NotNil(route.Gateway)
	s.Require().Equal(cidr.IP, route.LocalIP)
}

func (s *RoutingSuite) printRoutingTable(ctx context.Context) { //nolint:unused // Useful for debugging
	var err error
	// Print out the routing table for debugging
	switch runtime.GOOS {
	case "darwin":
		err = dexec.CommandContext(ctx, "netstat", "-nr").Run()
	case "linux":
		err = dexec.CommandContext(ctx, "ip", "route", "show", "table", "all").Run()
	case "windows":
		err = dexec.CommandContext(ctx, "route", "print").Run()
	}
	s.Require().NoError(err)
	// Print out the table rules for debugging
	switch runtime.GOOS {
	case "darwin":
		err = dexec.CommandContext(ctx, "netstat", "-nr", "-f", "inet", "-f", "inet6").Run()
	case "linux":
		err = dexec.CommandContext(ctx, "ip", "rule", "show").Run()
	case "windows":
		err = dexec.CommandContext(ctx, "netsh", "interface", "ipv4", "show", "route").Run()
	}
	s.Require().NoError(err)
}

func (s *RoutingSuite) runRouter(pCtx context.Context, args ...string) (string, context.CancelFunc, error) {
	pc, _, _, ok := runtime.Caller(1)
	s.Require().True(ok)
	details := runtime.FuncForPC(pc)
	pCtx = dlog.WithField(pCtx, "test", regexp.MustCompile(`^.*\.(.*)$`).ReplaceAllString(details.Name(), "$1"))

	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return "", nil, err
	}
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return "", nil, err
	}

	pCtx, pCancel := context.WithCancel(pCtx)

	var cmd *dexec.Cmd
	if runtime.GOOS == "windows" {
		cmd = dexec.CommandContext(pCtx, "testdata\\router\\router.exe", args...)
	} else {
		args = append([]string{"./testdata/router/router"}, args...)
		cmd = dexec.CommandContext(pCtx, "sudo", args...)
	}

	cmd.Stdout = outWrite
	cmd.Stdin = inRead
	err = cmd.Start()
	if err != nil {
		pCancel()
		return "", nil, err
	}
	pCtx = dlog.WithField(pCtx, "pid", cmd.Process.Pid)

	wg := dgroup.NewGroup(pCtx, dgroup.GroupConfig{EnableSignalHandling: true})

	readyCh := make(chan string)
	defer close(readyCh)
	errCh := make(chan error)
	doneCh := make(chan struct{})
	cmdCtx, cmdCancel := context.WithCancel(pCtx)
	wg.Go("cmdCleanup", func(ctx context.Context) error {
		defer func() {
			inWrite.Close()
			outRead.Close()
			outWrite.Close()
			inRead.Close()
		}()
		select {
		case <-cmdCtx.Done():
		case <-doneCh:
		}

		_, err := inWrite.WriteString("\n")
		if err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
		return nil
	})
	wg.Go("readStdout", func(ctx context.Context) error {
		scanner := bufio.NewScanner(outRead)
		for scanner.Scan() {
			text := scanner.Text()
			if strings.HasPrefix(text, "Device: ") {
				readyCh <- strings.TrimSpace(strings.TrimPrefix(text, "Device: "))
			}
			dlog.Infof(ctx, "router: %s", text)
		}
		dlog.Infof(ctx, "router: EOF")
		return nil
	})
	wg.Go("run", func(ctx context.Context) error {
		defer close(doneCh)
		return cmd.Wait()
	})
	go func() {
		defer close(errCh)
		errCh <- wg.Wait()
	}()

	canceler := func() {
		defer pCancel()
		cmdCancel()
		select {
		case <-time.After(10 * time.Second):
			s.FailNow("Router did not exit in time")
		case err := <-errCh:
			s.Require().NoError(err)
		}
	}

	select {
	case device := <-readyCh:
		return device, canceler, nil
	case err := <-errCh:
		canceler()
		return "", nil, err
	case <-pCtx.Done():
		canceler()
		return "", nil, pCtx.Err()
	case <-time.After(45 * time.Second):
		canceler()
		return "", nil, fmt.Errorf("router did not start in time")
	}
}
