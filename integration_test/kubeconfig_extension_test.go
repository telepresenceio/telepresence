package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
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

	s.TelepresenceConnect(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	itest.TelepresenceQuitOk(ctx)
	rq := s.Require()
	rq.True(len(st.RootDaemon.Subnets) > 0)
	svcCIDR := st.RootDaemon.Subnets[0]
	ones, bits := svcCIDR.Mask.Size()
	if ones != 16 || bits != 32 {
		s.T().Skip("test requires an IPv4 service subnet with a 16 bit mask")
	}

	base := svcCIDR.IP.Mask(svcCIDR.Mask)
	largeCIDR := &net.IPNet{
		IP:   base,
		Mask: net.CIDRMask(24, 32),
	}
	smallCIDR := &net.IPNet{
		IP:   base,
		Mask: net.CIDRMask(28, 32),
	}
	// testIP is an IP that is covered by smallCIDR
	testIP := &net.IPNet{
		IP:   net.IP{base[0], base[1], 0, 4},
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
			alsoProxy:  []string{largeCIDR.String()},
			neverProxy: []string{smallCIDR.String()},
			expectEq:   true,
		},
		"Also Proxy wins": {
			alsoProxy:  []string{smallCIDR.String()},
			neverProxy: []string{largeCIDR.String()},
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

func (s *notConnectedSuite) Test_AlsoNeverProxyDocker() {
	if s.IsCI() && !(runtime.GOOS == "linux" && runtime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}
	alsoProxy := []string{"10.128.0.0/16"}
	neverProxy := []string{"10.128.0.0/24"}
	ctx := itest.WithKubeConfigExtension(s.Context(), func(cluster *api.Cluster) map[string]any {
		return map[string]any{
			"never-proxy": neverProxy,
			"also-proxy":  alsoProxy,
		}
	})
	cidrsToStrings := func(cidrs []*iputil.Subnet) []string {
		ss := make([]string, len(cidrs))
		for i, cidr := range cidrs {
			ss[i] = cidr.String()
		}
		return ss
	}
	s.TelepresenceConnect(ctx, "--context", "extra", "--docker")
	defer itest.TelepresenceQuitOk(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	s.True(slice.ContainsAll(cidrsToStrings(st.ContainerizedDaemon.AlsoProxy), alsoProxy))
	s.True(slice.ContainsAll(cidrsToStrings(st.ContainerizedDaemon.NeverProxy), neverProxy))
}

func (s *notConnectedSuite) Test_DNSSuffixRules() {
	if s.IsCI() && runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		s.T().Skip("The DNS on the linux-arm64 GitHub runner is not configured correctly")
	}

	const randomName = "zwslkjsdf"
	const randomDomain = ".xnrqj"
	tests := []struct {
		name             string
		domainName       string
		includeSuffixes  []string
		excludeSuffixes  []string
		wantedLogEntry   []string
		mustHaveWanted   bool
		unwantedLogEntry []string
	}{
		{
			"default-exclude-com",
			randomName + ".com",
			nil,
			nil,
			[]string{
				`Cluster DNS excluded by exclude-suffix ".com" for name "` + randomName + `.com"`,
			},
			false,
			[]string{
				`Lookup A "` + randomName + `.com`,
			},
		},
		{
			"default-exclude-random-domain",
			randomName + randomDomain,
			nil,
			nil,
			[]string{
				`Cluster DNS excluded for name "` + randomName + randomDomain + `". No inclusion rule was matched`,
			},
			false,
			[]string{
				`Lookup A "` + randomName + randomDomain,
			},
		},
		{
			"include-random-domain",
			randomName + randomDomain,
			[]string{randomDomain},
			dns.DefaultExcludeSuffixes,
			[]string{
				`Cluster DNS included by include-suffix "` + randomDomain + `" for name "` + randomName + randomDomain,
				`Lookup A "` + randomName + randomDomain,
			},
			true,
			nil,
		},
		{
			"equally specific include overrides exclude",
			randomName + ".org",
			[]string{".org"},
			dns.DefaultExcludeSuffixes,
			[]string{
				`Cluster DNS included by include-suffix ".org" (overriding exclude-suffix ".org") for name "` + randomName + `.org"`,
				`Lookup A "` + randomName + `.org."`,
			},
			true,
			nil,
		},
		{
			"more specific include overrides exclude",
			randomName + ".my-domain.org",
			[]string{".my-domain.org"},
			dns.DefaultExcludeSuffixes,
			[]string{
				`Cluster DNS included by include-suffix ".my-domain.org" (overriding exclude-suffix ".org") for name "` + randomName + `.my-domain.org"`,
				`Lookup A "` + randomName + `.my-domain.org."`,
			},
			true,
			nil,
		},
		{
			"more specific exclude overrides include",
			randomName + ".my-domain.org",
			[]string{".org"},
			[]string{".com", ".my-domain.org"},
			[]string{
				`Cluster DNS excluded by exclude-suffix ".my-domain.org" for name "` + randomName + `.my-domain.org"`,
			},
			true,
			[]string{
				`Lookup A "` + randomName + `.my-domain.org."`,
			},
		},
	}
	logFile := filepath.Join(filelocation.AppUserLogDir(s.Context()), "daemon.log")

	for _, tt := range tests {
		tt := tt
		s.Run(tt.name, func() {
			ctx := itest.WithKubeConfigExtension(s.Context(), func(cluster *api.Cluster) map[string]any {
				return map[string]any{
					"dns": map[string][]string{
						"exclude-suffixes": tt.excludeSuffixes,
						"include-suffixes": tt.includeSuffixes,
					},
				}
			})
			require := s.Require()

			s.TelepresenceConnect(ctx, "--context", "extra")
			defer itest.TelepresenceDisconnectOk(ctx)

			// Check that config view -c includes the includeSuffixes
			var cfg client.SessionConfig
			stdout := itest.TelepresenceOk(ctx, "config", "view", "--client-only", "--output", "json")
			require.NoError(json.Unmarshal([]byte(stdout), &cfg))
			require.Equal(cfg.DNS.ExcludeSuffixes, tt.excludeSuffixes)
			require.Equal(cfg.DNS.IncludeSuffixes, tt.includeSuffixes)

			rootLog, err := os.Open(logFile)
			require.NoError(err)
			defer rootLog.Close()

			// Figure out where the current end of the logfile is. This must be done before any
			// of the tests run because the queries that the DNS resolver receives are dependent
			// on how the system's DNS resolver handle search paths and caching.
			st, err := rootLog.Stat()
			s.Require().NoError(err)
			pos := st.Size()

			short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			_, _ = net.DefaultResolver.LookupIPAddr(short, tt.domainName)

			// Give query time to reach telepresence and produce a log entry
			dtime.SleepWithContext(ctx, 100*time.Millisecond)

			for _, wl := range tt.wantedLogEntry {
				_, err = rootLog.Seek(pos, io.SeekStart)
				require.NoError(err)
				scn := bufio.NewScanner(rootLog)
				found := false

				// mustHaveWanted caters for cases where the default behavior from the system's resolver
				// is to not send unwanted queries to our resolver at all (based on search and routes).
				// It is forced to true for inclusion tests.
				mustHaveWanted := tt.mustHaveWanted
				for scn.Scan() {
					txt := scn.Text()
					if strings.Contains(txt, wl) {
						found = true
						break
					}
					if !mustHaveWanted {
						if strings.Contains(txt, " ServeDNS ") && strings.Contains(txt, tt.domainName) {
							mustHaveWanted = true
						}
					}
				}
				s.Truef(found || !mustHaveWanted, "Unable to find %q", wl)
			}

			for _, wl := range tt.unwantedLogEntry {
				_, err = rootLog.Seek(pos, io.SeekStart)
				require.NoError(err)
				scn := bufio.NewScanner(rootLog)
				found := false
				for scn.Scan() {
					if strings.Contains(scn.Text(), wl) {
						found = true
						break
					}
				}
				s.Falsef(found, "Found unwanted %q", wl)
			}
		})
	}
}
