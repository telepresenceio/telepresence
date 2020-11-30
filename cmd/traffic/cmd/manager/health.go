package manager

import (
	"context"

	"google.golang.org/grpc/health/grpc_health_v1"
)

// Perhaps replace this health check stuff with something more normal, i.e.
// based on HTTP, since we'll likely be running the Injector as an HTTP service
// from this same executable anyhow.

type HealthChecker struct{}

func (s *HealthChecker) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

func (s *HealthChecker) Watch(_ *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	return stream.Send(&grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	})
}
