package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// Per-connection server options for the external listener, hardcoded rather
// than Helm values to bound what a single connection can cost the manager.
const (
	// externalMaxConcurrentStreams bounds concurrent RPCs per connection.
	externalMaxConcurrentStreams = 100

	// externalMaxHeaderListSize bounds the uncompressed size of a single request's
	// gRPC/HTTP2 headers, well below grpc-go's own default (~16MiB).
	externalMaxHeaderListSize = 1 << 20 // 1MiB

	// externalKeepaliveMinTime is the minimum interval a client may send keepalive
	// pings at before the connection is closed as abusive (GOAWAY
	// ENHANCE_YOUR_CALM). It matches grpc-go's own server default, set explicitly so
	// the external listener doesn't silently inherit a future default change.
	externalKeepaliveMinTime = 5 * time.Minute

	// externalKeepaliveTime and externalKeepaliveTimeout govern the server's own
	// keepalive pings to the client, bounding how long a half-open connection (e.g.
	// a client behind a NAT that silently dropped state) is kept around.
	externalKeepaliveTime    = 2 * time.Minute
	externalKeepaliveTimeout = 20 * time.Second
)

// serveExternal serves the external client-only TLS gRPC listener when
// env.ExternalPort != 0. Enabling it requires ModeEnforcing and
// ExternalTLSCertDir; either missing is a fatal startup error, not a warning.
func serveExternal(ctx context.Context, svc Service) error {
	env := managerutil.GetEnv(ctx)
	if env.ExternalPort == 0 {
		return nil
	}
	if env.AuthenticationMode != auth.ModeEnforcing {
		return fmt.Errorf(
			"externalEndpoint.enabled requires security.authentication.mode: enforcing (got %q); "+
				"the external listener has no other access control", env.AuthenticationMode)
	}
	if env.ExternalTLSCertDir == "" {
		return errors.New(
			"externalEndpoint.enabled requires externalEndpoint.tls to be configured (EXTERNAL_TLS_CERT_DIR is unset)")
	}

	ki := k8sapi.GetK8sInterface(ctx)
	caPool := auth.NewClientCAPool(ctx, ki)
	tracker := auth.NewExternalConnTracker()
	// OnChange must be registered before Start (see ClientCAPool.OnChange); this pool
	// is dedicated to the external listener, so it doesn't conflict with the
	// mintedTokens.InvalidateAll callback the x509 auth-only listener's own pool
	// carries.
	caPool.OnChange(tracker.CloseStale)
	go caPool.Start(ctx)

	metrics := auth.NewMetrics()
	authenticator := auth.NewAuthenticator(ki, auth.WithMetrics(metrics))
	inner := auth.NewInterceptor(authenticator, auth.ModeEnforcing)
	extInterceptor := auth.NewExternalInterceptor(inner, caPool, metrics)

	creds := auth.NewExternalTransportCredentials(env.ExternalTLSCertDir, caPool, tracker)

	opts := []grpc.ServerOption{
		grpc.Creds(creds),
		grpc.MaxConcurrentStreams(externalMaxConcurrentStreams),
		grpc.MaxHeaderListSize(externalMaxHeaderListSize),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: externalKeepaliveMinTime}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: externalKeepaliveTime, Timeout: externalKeepaliveTimeout}),
	}
	if mz, ok := env.GrpcMaxReceiveSize.AsInt64(); ok {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}

	// Mirrors serveHTTP: the auth interceptor is the innermost one, running after the
	// context/logging/error interceptors NewWithAuth always installs.
	grpcSrv := server.NewWithAuth(ctx, &server.Interceptors{Unary: extInterceptor.Unary(), Stream: extInterceptor.Stream()}, opts...)
	// Only the client-only wrapper and health are ever registered here: the internal
	// service (agent RPCs, WatchQuicBackends, ...) must never be reachable on a
	// publicly exposed listener.
	rpc.RegisterManagerServer(grpcSrv, newExternalService(svc))
	grpc_health_v1.RegisterHealthServer(grpcSrv, &HealthChecker{})

	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(int(env.ExternalPort)))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("external listener: listen on %s: %w", addr, err)
	}
	ln = auth.NewIdleTimeoutListener(ln)

	clog.Infof(ctx, "External TLS listener started on %s", ln.Addr())
	defer clog.Info(ctx, "External TLS listener stopped")
	return server.Serve(ctx, grpcSrv, ln)
}
