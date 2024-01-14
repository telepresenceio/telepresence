package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

func getClusterIPs(cluster *api.Cluster) ([]net.IP, error) {
	var ips []net.IP
	svcUrl, err := url.Parse(cluster.Server)
	if err != nil {
		return nil, err
	}
	hostname := svcUrl.Hostname()
	if rawIP := net.ParseIP(hostname); rawIP != nil {
		ips = []net.IP{rawIP}
	} else {
		ips, err = net.LookupIP(hostname)
		if err != nil {
			return nil, err
		}
	}
	return ips, nil
}

func (s *notConnectedSuite) Test_APIServerIsProxied() {
	ctx := s.Context()
	require := s.Require()
	defaultGW, err := routing.DefaultRoute(ctx)
	require.NoError(err)
	var ips []net.IP

	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		var apiServers []string
		var err error
		ips, err = getClusterIPs(cluster)
		require.NoError(err)
		for _, ip := range ips {
			if ip.IsLoopback() {
				s.T().Skipf("test can't run on host with a loopback cluster IP %s", ip)
			}
			if len(ip) == 16 {
				apiServers = append(apiServers, fmt.Sprintf(`%s/96`, ip))
			} else {
				apiServers = append(apiServers, fmt.Sprintf(`%s/24`, ip))
			}
			if defaultGW.Routes(ip) {
				s.T().Skipf("test can't run on host with route %s and cluster IP %s", defaultGW.String(), ip)
			}
		}
		return map[string]any{"also-proxy": apiServers}
	})

	s.TelepresenceConnect(ctx, "--context", "extra")

	expectedLen := len(ips)
	expect := regexp.MustCompile(`Also Proxy\s*:\s*\((\d+) subnets\)`)
	s.Eventually(func() bool {
		stdout, stderr, err := itest.Telepresence(ctx, "status")
		if err == nil {
			if m := expect.FindStringSubmatch(stdout); m != nil && m[1] == strconv.Itoa(expectedLen) {
				return true
			}
			dlog.Infof(ctx, "%q does not match %q to %d subnets", stdout, expect, expectedLen)
		} else {
			dlog.Errorf(ctx, "%s: %v", stderr, err)
		}
		return false
	}, 30*time.Second, 3*time.Second, fmt.Sprintf("did not find %d also-proxied subnets", expectedLen))

	status := itest.TelepresenceStatusOk(ctx)
	require.Len(status.RootDaemon.AlsoProxy, expectedLen)
	for _, ip := range ips {
		rng := make(net.IP, len(ip))
		copy(rng[:], ip)
		rng[len(rng)-1] = 0
		expectedValue := &iputil.Subnet{
			IP:   rng,
			Mask: net.CIDRMask(24, 32),
		}
		require.Contains(status.RootDaemon.AlsoProxy, expectedValue)
	}
}

func (s *notConnectedSuite) Test_NeverProxy() {
	require := s.Require()
	ctx := s.Context()

	svcName := "echo-never-proxy"
	itest.ApplyEchoService(ctx, svcName, s.AppNamespace(), 8080)
	defer itest.DeleteSvcAndWorkload(ctx, "deploy", svcName, s.AppNamespace())
	ip, err := itest.Output(ctx, "kubectl",
		"--namespace", s.AppNamespace(),
		"get", "svc", svcName,
		"-o",
		"jsonpath={.spec.clusterIP}")
	require.NoError(err)
	mask := 32
	if s.IsIPv6() {
		mask = 128
	}
	var ips []net.IP
	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		var err error
		ips, err = getClusterIPs(cluster)
		require.NoError(err)
		return map[string]any{"never-proxy": []string{fmt.Sprintf("%s/%d", ip, mask)}}
	})
	s.TelepresenceConnect(ctx, "--context", "extra")

	neverProxiedCount := 1

	// The cluster's IP address will be never proxied unless it's a loopback, so we gotta account for that.
	for _, cip := range ips {
		if !cip.IsLoopback() {
			neverProxiedCount++
		}
	}
	s.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "status")
		if err != nil {
			return false
		}
		m := regexp.MustCompile(`Never Proxy\s*:\s*\((\d+) subnets\)`).FindStringSubmatch(stdout)
		return m != nil && m[1] == strconv.Itoa(neverProxiedCount)
	}, 5*time.Second, 1*time.Second, fmt.Sprintf("did not find %d never-proxied subnets", neverProxiedCount))

	s.Eventually(func() bool {
		status, err := itest.TelepresenceStatus(ctx)
		return err == nil && status.RootDaemon != nil && len(status.RootDaemon.NeverProxy) == neverProxiedCount
	}, 5*time.Second, 1*time.Second, fmt.Sprintf("did not find %d never-proxied subnets in json status", neverProxiedCount))

	s.Eventually(func() bool {
		return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", ip) != nil
	}, 15*time.Second, 2*time.Second, fmt.Sprintf("never-proxied IP %s is reachable", ip))
}

func (s *notConnectedSuite) Test_ConflictingProxies() {
	ctx := s.Context()

	testIP := &net.IPNet{
		IP:   net.ParseIP("10.128.0.32"),
		Mask: net.CIDRMask(32, 32),
	}
	// We don't really care if we can't route this with TP disconnected provided the result is the same once we connect
	originalRoute, _ := routing.GetRoute(ctx, testIP)
	for name, t := range map[string]struct {
		alsoProxy  []string
		neverProxy []string
		expectEq   bool
	}{
		"Never Proxy wins": {
			alsoProxy:  []string{"10.128.0.0/16"},
			neverProxy: []string{"10.128.0.0/24"},
			expectEq:   true,
		},
		"Also Proxy wins": {
			alsoProxy:  []string{"10.128.0.0/24"},
			neverProxy: []string{"10.128.0.0/16"},
			expectEq:   false,
		},
	} {
		s.Run(name, func() {
			ctx := itest.WithKubeConfigExtension(s.Context(), func(cluster *api.Cluster) map[string]any {
				return map[string]any{
					"never-proxy": t.neverProxy,
					"also-proxy":  t.alsoProxy,
				}
			})
			s.TelepresenceConnect(ctx, "--context", "extra")
			defer itest.TelepresenceQuitOk(ctx)
			s.Eventually(func() bool {
				newRoute, err := routing.GetRoute(ctx, testIP)
				if err != nil {
					return false
				}
				if t.expectEq {
					if originalRoute.Interface != nil {
						return newRoute.Interface != nil && originalRoute.Interface.Name == newRoute.Interface.Name
					}
					return newRoute.Interface == nil
				}
				if newRoute.Interface == nil {
					return false
				}
				if originalRoute.Interface == nil {
					return true
				}
				return newRoute.Interface.Name != originalRoute.Interface.Name
			}, 30*time.Second, 200*time.Millisecond)
		})
	}
}

func (s *notConnectedSuite) Test_DNSIncludes() {
	ctx := s.Context()

	ctx = itest.WithKubeConfigExtension(ctx, func(cluster *api.Cluster) map[string]any {
		return map[string]any{"dns": map[string][]string{"include-suffixes": {".org"}}}
	})
	require := s.Require()
	logFile := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")

	s.TelepresenceConnect(ctx, "--context", "extra")
	defer itest.TelepresenceDisconnectOk(ctx)

	// Check that config view -c includes the includeSuffixes
	stdout := itest.TelepresenceOk(ctx, "config", "view", "--client-only")
	require.Contains(stdout, "    includeSuffixes:\n        - .org")

	retryCount := 0
	s.Eventually(func() bool {
		// Test with ".org" suffix that was added as an include-suffix
		host := fmt.Sprintf("zwslkjsdf-%d.org", retryCount)
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, _ = net.DefaultResolver.LookupIPAddr(short, host)

		// Give query time to reach telepresence and produce a log entry
		dtime.SleepWithContext(ctx, 100*time.Millisecond)

		rootLog, err := os.Open(logFile)
		require.NoError(err)
		defer rootLog.Close()

		scanFor := fmt.Sprintf(`Lookup A "%s."`, host)
		scn := bufio.NewScanner(rootLog)
		for scn.Scan() {
			if strings.Contains(scn.Text(), scanFor) {
				return true
			}
		}
		retryCount++
		return false
	}, 30*time.Second, time.Second, "daemon.log does not contain expected LookupHost entry")
}
