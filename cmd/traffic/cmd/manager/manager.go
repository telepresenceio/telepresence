package manager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var (
	DisplayName                 = "OSS Traffic Manager"               //nolint:gochecknoglobals // extension point
	NewServiceFunc              = NewService                          //nolint:gochecknoglobals // extension point
	WithAgentImageRetrieverFunc = managerutil.WithAgentImageRetriever //nolint:gochecknoglobals // extension point
)

// Main starts up the traffic manager and blocks until it ends.
func Main(ctx context.Context, _ ...string) error {
	ctx, err := managerutil.LoadEnv(ctx, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("failed to LoadEnv: %w", err)
	}
	env := managerutil.GetEnv(ctx)
	agentmap.GeneratorConfigFunc = env.GeneratorConfig
	return MainWithEnv(ctx)
}

func MainWithEnv(ctx context.Context) error {
	dlog.Infof(ctx, "%s %s [uid:%d,gid:%d]", DisplayName, version.Version, os.Getuid(), os.Getgid())

	env := managerutil.GetEnv(ctx)
	var tracer *tracing.TraceServer

	if env.TracingGrpcPort != 0 {
		var err error
		tracer, err = tracing.NewTraceServer(ctx, "traffic-manager",
			attribute.String("tel2.agent-image", env.QualifiedAgentImage()),
			attribute.String("tel2.managed-namespaces", strings.Join(env.ManagedNamespaces, ",")),
			attribute.String("k8s.namespace", env.ManagerNamespace),
			attribute.String("k8s.pod-ip", env.PodIP.String()),
		)
		if err != nil {
			return err
		}
		defer tracer.Shutdown(ctx)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("unable to get the Kubernetes InClusterConfig: %w", err)
	}
	cfg.WrapTransport = tracing.NewWrapperFunc()
	ki, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create the Kubernetes Interface from InClusterConfig: %w", err)
	}
	ctx = k8sapi.WithK8sInterface(ctx, ki)

	ctx, imgRetErr := WithAgentImageRetrieverFunc(ctx, mutator.RegenerateAgentMaps)
	mgr, ctx, err := NewServiceFunc(ctx)
	if err != nil {
		return fmt.Errorf("unable to initialize traffic manager: %w", err)
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
		SoftShutdownTimeout:  5 * time.Second,
	})

	g.Go("cli-config", mgr.runConfigWatcher)

	// Serve HTTP (including gRPC)
	g.Go("httpd", mgr.serveHTTP)

	g.Go("prometheus", mgr.servePrometheus)

	if imgRetErr != nil {
		dlog.Errorf(ctx, "unable to initialize agent injector: %v", imgRetErr)
	} else {
		g.Go("agent-injector", mutator.ServeMutator)
	}

	g.Go("session-gc", mgr.runSessionGCLoop)

	if tracer != nil {
		g.Go("tracer-grpc", func(c context.Context) error {
			return tracer.ServeGrpc(c, env.TracingGrpcPort)
		})
	}

	// Wait for exit
	return g.Wait()
}

// ServePrometheus serves Prometheus metrics if env.PrometheusPort != 0.
func (s *service) servePrometheus(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	if env.PrometheusPort == 0 {
		dlog.Info(ctx, "Prometheus metrics server not started")
		return nil
	}
	newGaugeFunc := func(n, h string, f func() int) {
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Name: n,
			Help: h,
		}, func() float64 { return float64(f()) })
	}
	newGaugeFunc("agent_count", "Number of connected traffic agents", s.state.CountAgents)
	newGaugeFunc("client_count", "Number of connected clients", s.state.CountClients)
	newGaugeFunc("intercept_count", "Number of active intercepts", s.state.CountIntercepts)
	newGaugeFunc("session_count", "Number of sessions", s.state.CountSessions)
	newGaugeFunc("tunnel_count", "Number of tunnels", s.state.CountTunnels)

	newGaugeFunc("active_http_request_count", "Number of currently served http requests", func() int {
		return int(atomic.LoadInt32(&s.activeHttpRequests))
	})

	newGaugeFunc("active_grpc_request_count", "Number of currently served gRPC requests", func() int {
		return int(atomic.LoadInt32(&s.activeGrpcRequests))
	})

	sc := &dhttp.ServerConfig{
		Handler: promhttp.Handler(),
	}
	dlog.Infof(ctx, "Prometheus metrics server started on port: %d", env.PrometheusPort)
	defer dlog.Info(ctx, "Prometheus metrics server stopped")
	return sc.ListenAndServe(ctx, fmt.Sprintf("%s:%d", env.ServerHost, env.PrometheusPort))
}

func (s *service) serveHTTP(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	host := env.ServerHost
	port := env.ServerPort
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.StreamInterceptor(otelgrpc.StreamServerInterceptor()),
	}
	if mz, ok := env.MaxReceiveSize.AsInt64(); ok {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}

	grpcHandler := grpc.NewServer(opts...)
	httpHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello World from: %s\n", r.URL.Path)
	}))

	sc := &dhttp.ServerConfig{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				atomic.AddInt32(&s.activeGrpcRequests, 1)
				grpcHandler.ServeHTTP(w, r)
				atomic.AddInt32(&s.activeGrpcRequests, -1)
			} else {
				atomic.AddInt32(&s.activeHttpRequests, 1)
				httpHandler.ServeHTTP(w, r)
				atomic.AddInt32(&s.activeHttpRequests, -1)
			}
		}),
	}
	s.self.RegisterServers(grpcHandler)
	return sc.ListenAndServe(ctx, fmt.Sprintf("%s:%d", host, port))
}

func (s *service) RegisterServers(grpcHandler *grpc.Server) {
	rpc.RegisterManagerServer(grpcHandler, s)
	grpc_health_v1.RegisterHealthServer(grpcHandler, &HealthChecker{})
}

func (s *service) runSessionGCLoop(ctx context.Context) error {
	// Loop calling Expire
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.expire(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}
