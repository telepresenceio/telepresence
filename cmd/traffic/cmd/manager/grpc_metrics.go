package manager

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// grpcMetrics are the process-wide gRPC request metrics of all listeners,
// registered once.
var grpcMetrics = struct { //nolint:gochecknoglobals // prometheus metrics registered once at load
	active   prometheus.Gauge
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}{
	active: promauto.NewGauge(prometheus.GaugeOpts{
		Name: "telepresence_active_grpc_request_count",
		Help: "Number of currently served gRPC requests",
	}),
	requests: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "telepresence_grpc_request_count",
		Help: "Number of completed gRPC requests by method and status code",
	}, []string{"listener", "method", "code"}),
	duration: promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "telepresence_grpc_request_duration_seconds",
		Help:    "Duration of completed unary gRPC requests by method",
		Buckets: prometheus.DefBuckets,
	}, []string{"listener", "method"}),
}

// grpcMetricsOptions returns server options that record the requests of the
// named listener ("internal" or "external") in grpcMetrics. They must precede
// the interceptors that server.NewWithAuth installs, so that the recorded
// code is that of the status sent to the client.
func grpcMetricsOptions(listener string) []grpc.ServerOption {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		grpcMetrics.active.Inc()
		defer grpcMetrics.active.Dec()
		start := time.Now()
		v, err := handler(ctx, req)
		grpcMetrics.duration.WithLabelValues(listener, info.FullMethod).Observe(time.Since(start).Seconds())
		grpcMetrics.requests.WithLabelValues(listener, info.FullMethod, status.Code(err).String()).Inc()
		return v, err
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		grpcMetrics.active.Inc()
		defer grpcMetrics.active.Dec()
		err := handler(srv, ss)
		grpcMetrics.requests.WithLabelValues(listener, info.FullMethod, status.Code(err).String()).Inc()
		return err
	}
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(unary), grpc.ChainStreamInterceptor(stream)}
}
