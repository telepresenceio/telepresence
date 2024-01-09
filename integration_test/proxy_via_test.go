package integration_test

import (
	"net"
	"regexp"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func (s *notConnectedSuite) Test_ProxyVia() {
	ctx := s.Context()
	svc := "echo"
	s.ApplyApp(ctx, "echo-w-hostalias", "deploy/"+svc)
	defer func() {
		s.DeleteSvcAndWorkload(ctx, "deploy", svc)
	}()

	s.TelepresenceConnect(ctx, "--proxy-via", "127.0.0.1/32="+svc)
	defer itest.TelepresenceQuitOk(ctx)

	expectedOutput := regexp.MustCompile("Host: echo-home:8080")
	_, virtualIPSubnet, err := net.ParseCIDR(client.GetConfig(ctx).Cluster().VirtualIPSubnet)
	s.Require().NoError(err)
	s.Eventually(func() bool {
		// echo-home will resolve to 127.0.0.1 and then be translated into a virtual IP
		ips, err := net.LookupIP("echo-home")
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		if len(ips) != 1 {
			dlog.Error(ctx, "LookupIP did not return one IP")
			return false
		}
		vip := ips[0]
		dlog.Infof(ctx, "echo-home uses IP %s", vip)
		if !virtualIPSubnet.Contains(vip) {
			dlog.Errorf(ctx, "virtualIPSubnet %s does not contain %s", virtualIPSubnet, vip)
			return false
		}
		out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", "echo-home:8080")
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		dlog.Info(ctx, out)
		return expectedOutput.MatchString(out)
	}, 10*time.Second, 2*time.Second)
}
