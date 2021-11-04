package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_APIServerIsProxied() {
	require := s.Require()
	var ips []net.IP
	ctx := itest.WithKubeConfigExtension(s.Context(), func(cluster *api.Cluster) map[string]interface{} {
		svcUrl, err := url.Parse(cluster.Server)
		require.NoError(err)
		hostname := svcUrl.Hostname()
		if rawIP := net.ParseIP(hostname); rawIP != nil {
			ips = []net.IP{rawIP}
		} else {
			ips, err = net.LookupIP(hostname)
			require.NoError(err)
		}
		var apiServers []string
		for _, ip := range ips {
			apiServers = append(apiServers, fmt.Sprintf(`%s/24`, ip))
		}
		return map[string]interface{}{"also-proxy": apiServers}
	})

	itest.TelepresenceOk(ctx, "connect")
	defer itest.TelepresenceQuitOk(ctx)
	stdout := itest.TelepresenceOk(ctx, "status")
	require.Contains(stdout, fmt.Sprintf("Also Proxy: (%d subnets)", len(ips)))
	for _, ip := range ips {
		rng := make(net.IP, len(ip))
		copy(rng[:], ip)
		rng[len(rng)-1] = 0
		require.Contains(stdout, fmt.Sprintf("- %s/24", rng), fmt.Sprintf("Expecting to find '- %s/24'", rng))
	}
	require.Contains(stdout, "networking to the cluster is enabled")
}

func (s *notConnectedSuite) Test_DNSIncludes() {
	ctx := itest.WithKubeConfigExtension(s.Context(), func(cluster *api.Cluster) map[string]interface{} {
		return map[string]interface{}{"dns": map[string][]string{"include-suffixes": {".org"}}}
	})
	require := s.Require()
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	logFile := filepath.Join(logDir, "daemon.log")

	itest.TelepresenceOk(ctx, "connect")
	defer itest.TelepresenceQuitOk(ctx)

	retryCount := 0
	s.Eventually(func() bool {
		// Test with ".org" suffix that was added as an include-suffix
		host := fmt.Sprintf("zwslkjsdf-%d.org", retryCount)
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_ = itest.Run(short, "curl", "--silent", "--connect-timeout", "0.5", host)

		// Give query time to reach telepresence and produce a log entry
		dtime.SleepWithContext(ctx, 100*time.Millisecond)

		rootLog, err := os.Open(logFile)
		require.NoError(err)
		defer rootLog.Close()

		scanFor := fmt.Sprintf(`LookupHost "%s"`, host)
		scn := bufio.NewScanner(rootLog)
		for scn.Scan() {
			if strings.Contains(scn.Text(), scanFor) {
				return true
			}
		}
		retryCount++
		return false
	}, 10*time.Second, time.Second, "daemon.log does not contain expected LookupHost entry")
}
