package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

type interceptLocalhostSuite struct {
	itest.Suite
	itest.SingleService
	cancelLocal  context.CancelFunc
	defaultRoute *routing.Route
	port         int
}

func (s *interceptLocalhostSuite) SuiteName() string {
	return "InterceptLocalhost"
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &interceptLocalhostSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}

func (s *interceptLocalhostSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	var err error
	s.defaultRoute, err = routing.DefaultRoute(ctx)
	s.Require().NoError(err)
	dlog.Infof(ctx, "ip: %s: route: %s", s.defaultRoute.LocalIP, s.defaultRoute)
	s.port, s.cancelLocal = itest.StartLocalHttpEchoServerWithAddr(ctx, s.ServiceName(), net.JoinHostPort(s.defaultRoute.LocalIP.String(), "0"), nil)
}

func (s *interceptLocalhostSuite) TearDownSuite() {
	s.cancelLocal()
}

func (s *interceptLocalhostSuite) TestIntercept_WithCustomLocalhost() {
	ctx := s.Context()
	doRequest := func(ctx context.Context, host, port string) error {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/", net.JoinHostPort(host, port)), nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// If there was a response, make sure it's a 200
		s.Require().Equal(http.StatusOK, resp.StatusCode)
		return nil
	}
	// Make sure the IP address we think will respond is actually gonna respond, and that localhost won't
	s.Require().NoError(doRequest(ctx, s.defaultRoute.LocalIP.String(), strconv.Itoa(s.port)))
	s.Require().Error(doRequest(ctx, "127.0.0.1", strconv.Itoa(s.port)))

	// Run the intercept
	stdout := itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--port", strconv.Itoa(s.port), "--address", s.defaultRoute.LocalIP.String())
	defer itest.TelepresenceOk(ctx, "leave", s.ServiceName())

	s.Require().Contains(stdout, "Using Deployment "+s.ServiceName())
	itest.PingInterceptedEchoServer(ctx, s.ServiceName(), "80")
}
