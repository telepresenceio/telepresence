package integration_test

import (
	"context"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
)

func (s *connectedSuite) Test_PodWithSubdomain() {
	c := s.Context()
	s.ApplyApp(c, "echo-w-subdomain", "deploy/echo-subsonic")
	defer func() {
		s.NoError(s.Kubectl(c, "delete", "svc", "subsonic"))
		s.NoError(s.Kubectl(c, "delete", "deploy", "echo-subsonic"))
	}()

	lookupHost := func(host string) {
		var err error
		s.Eventually(func() bool {
			c, cancel := context.WithTimeout(c, 1800*time.Millisecond)
			defer cancel()
			dlog.Info(c, "LookupHost("+host+")")
			_, err = net.DefaultResolver.LookupHost(c, host)
			return err == nil
		}, 10*time.Second, 2*time.Second, "%s did not resolve: %v", host, err)
	}
	lookupHost("echo.subsonic." + s.AppNamespace())
	lookupHost("echo.subsonic." + s.AppNamespace() + ".svc.cluster.local")
}
