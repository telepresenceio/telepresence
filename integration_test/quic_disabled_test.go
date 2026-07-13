package integration_test

import (
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// quicTunnelDisabledSuite verifies the other half of the fallback story: a
// traffic-manager installed without quicTunnel.enabled (the chart default)
// must keep serving the tunnel over plain gRPC, exactly like before this
// feature existed, and "telepresence status" must say so.
//
// This is a separate suite (rather than a sub-scenario of quicTunnelSuite)
// because it needs its own traffic-manager install without the quicTunnel
// Helm flags, the same way nodeAgentSuite and nodeAgentNoInjectorSuite
// (node_agent_test.go, node_agent_no_injector_test.go) split by install
// flags instead of installing per-test.
type quicTunnelDisabledSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *quicTunnelDisabledSuite) SuiteName() string {
	return "QuicTunnelDisabled"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &quicTunnelDisabledSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *quicTunnelDisabledSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	// No quicTunnel.* settings: the chart default is quicTunnel.enabled=false.
	s.TelepresenceHelmInstallOK(ctx, false)
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
	s.TelepresenceConnect(ctx)
}

func (s *quicTunnelDisabledSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// Test_GRPCTransport verifies that with quicTunnel disabled, status reports
// the plain "grpc" transport (never "grpc (fallback)", since there was never
// a quic endpoint to fall back from) and that traffic through the tunnel
// still works.
func (s *quicTunnelDisabledSuite) Test_GRPCTransport() {
	ctx := s.Context()
	rq := s.Require()

	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", "echo-easy")
		return err == nil && strings.Contains(so, "Request served by")
	}, 30*time.Second, 2*time.Second, "echo-easy was not reachable through the gRPC tunnel")

	status := itest.TelepresenceStatusOk(ctx)
	rq.NotNil(status.RootDaemon)
	s.Equal("grpc", status.RootDaemon.TunnelTransport)
}

// Test_InterceptStillWorks verifies that a regular (sidecar) intercept works
// end-to-end with quicTunnel disabled, and that status keeps reporting the
// plain "grpc" transport throughout.
func (s *quicTunnelDisabledSuite) Test_InterceptStillWorks() {
	ctx := s.Context()
	const svc = "echo-easy"

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", strconv.Itoa(port), svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "detach", svc)
		}
	}()

	itest.PingInterceptedEchoServer(ctx, svc, "80")

	status := itest.TelepresenceStatusOk(ctx)
	s.Require().NotNil(status.RootDaemon)
	s.Equal("grpc", status.RootDaemon.TunnelTransport)

	itest.TelepresenceOk(ctx, "detach", svc)
	mustLeave = false
}
