package integration_test

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
)

func (s *connectedSuite) Test_SvcDomain() {
	c := s.Context()
	s.ApplyEchoService(c, "echo", 8080)
	defer s.DeleteSvcAndWorkload(c, "deploy", "echo")

	host := fmt.Sprintf("echo.%s.svc", s.AppNamespace())
	s.Eventuallyf(func() bool {
		c, cancel := context.WithTimeout(c, 1800*time.Millisecond)
		defer cancel()
		dlog.Info(c, "LookupHost("+host+")")
		_, err := net.DefaultResolver.LookupHost(c, host)
		return err == nil
	}, 10*time.Second, 2*time.Second, "%s did not resolve", host)
}
