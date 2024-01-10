package integration_test

import (
	"net"
	"regexp"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type proxyViaSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *proxyViaSuite) SuiteName() string {
	return "ProxyVia"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &proxyViaSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *proxyViaSuite) Test_ProxyVia() {
	ctx := s.Context()
	svc := "echo"
	s.ApplyApp(ctx, "echo-w-hostalias", "deploy/"+svc)
	defer func() {
		s.DeleteSvcAndWorkload(ctx, "deploy", svc)
	}()

	err := s.TelepresenceHelmInstall(ctx, false, "--set", "client.dns.includeSuffixes={mydomain.local}")
	s.Require().NoError(err)
	defer s.UninstallTrafficManager(ctx, s.ManagerNamespace())

	s.TelepresenceConnect(ctx, "--proxy-via", "127.0.0.1/32="+svc)
	defer itest.TelepresenceQuitOk(ctx)

	_, virtualIPSubnet, err := net.ParseCIDR(client.GetConfig(ctx).Cluster().VirtualIPSubnet)
	s.Require().NoError(err)

	tests := []struct {
		name           string
		hostName       string
		expectedOutput *regexp.Regexp
	}{
		{
			"single-label",
			"echo-home",
			regexp.MustCompile("Host: echo-home:8080"),
		},
		{
			"fully-qualified",
			"echo-home.mydomain.local",
			regexp.MustCompile("Host: echo-home.mydomain.local:8080"),
		},
	}
	for _, tt := range tests {
		tt := tt
		s.Run(tt.name, func() {
			rq := s.Require()
			var ips []net.IP
			rq.Eventually(func() bool {
				// hostname will resolve to 127.0.0.1 remotely and then be translated into a virtual IP
				ips, err = net.LookupIP(tt.hostName)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				if len(ips) != 1 {
					dlog.Error(ctx, "LookupIP did not return one IP")
					return false
				}
				return true
			}, 10*time.Second, 2*time.Second)
			vip := ips[0]
			dlog.Infof(ctx, "%s uses IP %s", tt.hostName, vip)
			rq.Truef(virtualIPSubnet.Contains(vip), "virtualIPSubnet %s does not contain %s", virtualIPSubnet, vip)

			out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", net.JoinHostPort(tt.hostName, "8080"))
			rq.NoError(err)
			dlog.Info(ctx, out)
			rq.Regexp(tt.expectedOutput, out)
		})
	}
}
