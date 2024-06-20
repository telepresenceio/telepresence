package integration_test

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
)

func (s *connectedSuite) Test_SvcDomain() {
	c := s.Context()
	s.ApplyEchoService(c, "echo", 8080)
	defer s.DeleteSvcAndWorkload(c, "deploy", "echo")

	host := fmt.Sprintf("echo.%s.svc", s.AppNamespace())
	s.Eventually(func() bool {
		c, cancel := context.WithTimeout(c, 1800*time.Millisecond)
		defer cancel()
		dlog.Info(c, "LookupHost("+host+")")
		_, err := net.DefaultResolver.LookupHost(c, host)
		return s.NoErrorf(err, "%s did not resolve", host)
	}, 10*time.Second, 2*time.Second)
}
