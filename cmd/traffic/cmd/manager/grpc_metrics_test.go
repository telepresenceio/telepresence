package manager

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

const (
	healthCheckMethod = "/grpc.health.v1.Health/Check"
	healthWatchMethod = "/grpc.health.v1.Health/Watch"
)

// metricsHealthServer fails Check with a plain error for the service "failing"
// and with NotFound for "unknown", and blocks Watch until the client goes away.
type metricsHealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	watching chan struct{}
}

func (s *metricsHealthServer) Check(_ context.Context, r *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	switch r.Service {
	case "failing":
		return nil, errors.New("check failed")
	case "unknown":
		return nil, status.Error(codes.NotFound, "unknown service")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

func (s *metricsHealthServer) Watch(_ *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	close(s.watching)
	<-stream.Context().Done()
	return stream.Context().Err()
}

// startMetricsHealthServer serves a metricsHealthServer, with the metrics of
// listener installed, and returns a client for it.
func startMetricsHealthServer(t *testing.T, listener string) (grpc_health_v1.HealthClient, *metricsHealthServer) {
	ctx := testutil.NewContext(t, false)
	lis := bufconn.Listen(64 * 1024)
	srv := server.New(ctx, grpcMetricsOptions(listener)...)
	hs := &metricsHealthServer{watching: make(chan struct{})}
	grpc_health_v1.RegisterHealthServer(srv, hs)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return grpc_health_v1.NewHealthClient(conn), hs
}

func requestCount(listener, method string, code codes.Code) float64 {
	return promtestutil.ToFloat64(grpcMetrics.requests.WithLabelValues(listener, method, code.String()))
}

func durationSampleCount(t *testing.T, listener, method string) uint64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "telepresence_grpc_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string, 2)
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["listener"] == listener && labels["method"] == method {
				return m.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func TestGrpcMetrics_Unary(t *testing.T) {
	const listener = "test-unary"
	client, _ := startMetricsHealthServer(t, listener)
	ctx := context.Background()
	okCount := requestCount(listener, healthCheckMethod, codes.OK)
	notFoundCount := requestCount(listener, healthCheckMethod, codes.NotFound)
	internalCount := requestCount(listener, healthCheckMethod, codes.Internal)
	sampleCount := durationSampleCount(t, listener, healthCheckMethod)

	for range 2 {
		_, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		require.NoError(t, err)
	}
	_, err := client.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "unknown"})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = client.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "failing"})
	require.Equal(t, codes.Internal, status.Code(err))

	assert.Equal(t, okCount+2, requestCount(listener, healthCheckMethod, codes.OK))
	assert.Equal(t, notFoundCount+1, requestCount(listener, healthCheckMethod, codes.NotFound))
	assert.Equal(t, internalCount+1, requestCount(listener, healthCheckMethod, codes.Internal), "a plain error is recorded as the code sent to the client")
	assert.Equal(t, sampleCount+4, durationSampleCount(t, listener, healthCheckMethod))
}

func TestGrpcMetrics_Stream(t *testing.T) {
	const listener = "test-stream"
	client, hs := startMetricsHealthServer(t, listener)
	active := promtestutil.ToFloat64(grpcMetrics.active)
	canceledCount := requestCount(listener, healthWatchMethod, codes.Canceled)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	require.NoError(t, err)
	<-hs.watching
	assert.Equal(t, active+1, promtestutil.ToFloat64(grpcMetrics.active))

	cancel()
	_, err = stream.Recv()
	require.Equal(t, codes.Canceled, status.Code(err))
	require.Eventually(t, func() bool {
		return requestCount(listener, healthWatchMethod, codes.Canceled) == canceledCount+1 && promtestutil.ToFloat64(grpcMetrics.active) == active
	}, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, durationSampleCount(t, listener, healthWatchMethod), "stream durations are not recorded")
}
