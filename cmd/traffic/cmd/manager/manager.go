package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var (
	DisplayName = "OSS Traffic Manager" //nolint:gochecknoglobals // extension point
	//nolint:gochecknoglobals // extension point
	IncrementInterceptCounterFunc = func(ctx context.Context, metric *prometheus.CounterVec, client, installId string, spec *rpc.InterceptSpec) {
		if metric != nil {
			labels := prometheus.Labels{
				"install_id":     installId,
				"intercept_type": "global",
			}
			if !managerutil.GetEnv(ctx).PrometheusDropClientLabel {
				labels["client"] = client
			}

			metric.With(labels).Inc()
		}
	}
)

// Main starts up the traffic manager and blocks until it ends.
func Main(ctx context.Context, _ ...string) error {
	ctx, err := managerutil.LoadEnv(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to LoadEnv: %w", err)
	}
	return MainWithEnv(ctx)
}

func MainWithEnv(ctx context.Context) (err error) {
	debug.SetTraceback("single")
	defer runtime.RecoverFromPanic(&err)

	clog.Infof(ctx, "%s %s [uid:%d,gid:%d]", DisplayName, version.Version, os.Getuid(), os.Getgid())

	env := managerutil.GetEnv(ctx)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("unable to get the Kubernetes InClusterConfig: %w", err)
	}
	ki, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create the Kubernetes Interface from InClusterConfig: %w", err)
	}
	ari, err := argorollouts.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create the Argo Rollouts Interface from InClusterConfig: %w", err)
	}
	return sigctx.DoWithSignalHandler(ctx, func(ctx context.Context) error {
		ctx = k8sapi.WithJoinedClientSetInterface(ctx, ki, ari)

		configWatcher := config.NewWatcher(env.ManagerNamespace)
		go func() {
			if err := configWatcher.Run(ctx); err != nil {
				clog.Error(ctx, err)
			}
		}()

		ctx, err = namespaces.InitContext(ctx, configWatcher.SelectorChannel())
		if err != nil {
			return err
		}

		// Ensure that the manager has access to shared informer factories for all relevant namespaces.
		//
		// This will make the informers more verbose. Good for debugging
		// l := klog.Level(6)
		// _ = l.Set("6")
		mgrFactory := false
		mns := namespaces.GetOrGlobal(ctx)
		global := len(mns) == 1 && mns[0] == ""
		if global {
			clog.Debug(ctx, "Using cluster wide informers")
		}
		for _, ns := range mns {
			ctx = informer.WithFactory(ctx, ns)
		}
		if !(global || slices.Contains(mns, env.ManagerNamespace)) {
			mgrFactory = true
			ctx = informer.WithFactory(ctx, env.ManagerNamespace)
		}

		var injectorCertGetter mutator.InjectorCertGetter
		if managerutil.AgentInjectorEnabled(ctx) {
			// The GetInjectorCertGetter and the mutator.Load both create SharedInformer instances
			// from informer factories, so these calls must be placed here in order for the factories
			// to start correctly.
			injectorCertGetter = mutator.GetInjectorCertGetter(ctx)
		}

		// We load the Map regardless of if the agent-injector is enabled or not. Intercepts can still
		// be added manually.
		watcher := mutator.Load(ctx)
		ctx = mutator.WithMap(ctx, watcher)

		if mgrFactory {
			f := informer.GetK8sFactory(ctx, env.ManagerNamespace)
			f.Start(ctx.Done())
			f.WaitForCacheSync(ctx.Done())
		}

		var err error
		if managerutil.AgentInjectorEnabled(ctx) {
			ctx, err = managerutil.WithAgentImageRetriever(ctx, mutator.GetMap(ctx).RegenerateAgentMaps)
			if err != nil {
				clog.Errorf(ctx, "unable to initialize agent injector: %v", err)
			}
		}

		g := log.NewGroup(ctx)
		mgr, err := NewService(ctx, g, configWatcher)
		if err != nil {
			return fmt.Errorf("unable to initialize traffic manager: %w", err)
		}
		watcher.SetConfigured()

		g.Go("config", namespaces.Listen)
		g.Go("prometheus", mgr.servePrometheus)

		if managerutil.AgentInjectorEnabled(ctx) {
			g.Go("agent-injector", func(ctx context.Context) error {
				if managerutil.GetAgentImageRetriever(ctx) == nil {
					return nil
				}
				return mutator.ServeMutator(ctx, g, injectorCertGetter)
			})
		}

		if managerutil.GetEnv(ctx).AgentMaxIdleTime != 0 {
			// only start the configmap updater if we set the agent max idle time, as we need to persist the latest agent state to the config map
			//  otherwise everything else is passively synced which is ok if we don't need to clean up idle agents
			g.Go("configmap-updater", mgr.runUpdateTrafficManagerConfigMapLoop)
		}

		// Serve HTTP (including gRPC). The gRPC server is started last so that its readiness probe can be used to determine when the
		// traffic-manager is fully configured and ready to serve traffic.
		g.Go("httpd", mgr.serveHTTP)

		// Wait for exit
		return g.Wait()
	})
}

func newCounterFunc[T int | uint64](n, h string, f func() T) {
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: n,
		Help: h,
	}, func() float64 { return float64(f()) })
}

func newGaugeFunc[T int | uint64](n, h string, f func() T) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: n,
		Help: h,
	}, func() float64 { return float64(f()) })
}

func newCounterVecFunc(n, h string, labels []string) *prometheus.CounterVec {
	counterVec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: n,
		Help: h,
	}, labels)
	prometheus.MustRegister(counterVec)
	return counterVec
}

func newGaugeVecFunc(n, h string, labels []string) *prometheus.GaugeVec {
	gaugeVec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: n,
		Help: h,
	}, labels)
	prometheus.MustRegister(gaugeVec)
	return gaugeVec
}

func IncrementCounter(ctx context.Context, metric *prometheus.CounterVec, client, installId string) {
	if metric != nil {
		labels := prometheus.Labels{"install_id": installId}
		if !managerutil.GetEnv(ctx).PrometheusDropClientLabel {
			labels["client"] = client
		}
		metric.With(labels).Inc()
	}
}

func SetGauge(ctx context.Context, metric *prometheus.GaugeVec, client, installId string, workload *string, value float64) {
	if metric != nil {
		labels := prometheus.Labels{
			"install_id": installId,
		}

		if !managerutil.GetEnv(ctx).PrometheusDropClientLabel {
			labels["client"] = client
		}

		if workload != nil {
			labels["workload"] = *workload
		}

		metric.With(labels).Set(value)
	}
}

// ServePrometheus serves Prometheus metrics if env.PrometheusPort != 0.
func (s *service) servePrometheus(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	if env.PrometheusPort == 0 {
		clog.Info(ctx, "Prometheus metrics server not started")
		return nil
	}
	newGaugeFunc("telepresence_agent_count", "Number of connected traffic agents", s.state.CountAgents)
	newGaugeFunc("telepresence_client_count", "Number of connected clients", s.state.CountClients)
	newGaugeFunc("telepresence_active_intercept_count", "Number of active intercepts", s.state.CountIntercepts)
	newGaugeFunc("telepresence_session_count", "Number of sessions", s.state.CountSessions)
	newGaugeFunc("telepresence_tunnel_count", "Number of tunnels", s.state.CountTunnels)
	newCounterFunc("telepresence_tunnel_ingress_bytes", "Number of bytes tunneled from clients", s.state.CountTunnelIngress)
	newCounterFunc("telepresence_tunnel_egress_bytes", "Number bytes tunneled to clients", s.state.CountTunnelEgress)

	newGaugeFunc("telepresence_active_http_request_count", "Number of currently served http requests", func() int {
		return int(atomic.LoadInt32(&s.activeHttpRequests))
	})

	newGaugeFunc("telepresence_active_grpc_request_count", "Number of currently served gRPC requests", func() int {
		return int(atomic.LoadInt32(&s.activeGrpcRequests))
	})

	labels := []string{"install_id"}
	if !managerutil.GetEnv(ctx).PrometheusDropClientLabel {
		labels = append(labels, "client")
	}
	s.state.SetPrometheusMetrics(
		newCounterVecFunc("telepresence_connect_count", "The total number of connects by user", labels),
		newGaugeVecFunc("telepresence_connect_active_status", "Flag to indicate when a connect is active. 1 for active, 0 for not active.", labels),
		newCounterVecFunc("telepresence_intercept_count", "The total number of intercepts by user", append(labels, "intercept_type")),
		newGaugeVecFunc("telepresence_intercept_active_status",
			"Flag to indicate when an intercept is active. 1 for active, 0 for not active.", append(labels, "workload")),
	)

	s.state.SetAllClientSessionsFinalizer(func(client *state.ClientSession) {
		SetGauge(ctx, s.state.GetConnectActiveStatus(), client.Name, client.InstallId, nil, 0)
	})

	s.state.SetAllInterceptsFinalizer(func(client *state.ClientSession, workload *string) {
		SetGauge(ctx, s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, workload, 0)
	})

	lg := clog.StdLogger(ctx, slog.LevelInfo)
	lg.SetPrefix(fmt.Sprintf("prometheus:%d", env.PrometheusPort))
	svc := http.Server{
		Handler:  promhttp.Handler(),
		ErrorLog: lg,
		Addr:     iputil.JoinHostPort(env.ServerHost, env.PrometheusPort),
	}

	go func() {
		defer clog.Info(ctx, "Prometheus metrics server stopped")
		err := svc.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			clog.Errorf(ctx, "Error serving Prometheus metrics: %v", err)
		}
	}()
	clog.Infof(ctx, "Prometheus metrics server started on port: %d", env.PrometheusPort)

	<-ctx.Done()
	return svc.Shutdown(context.Background())
}

func (s *service) serveHTTP(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	host := env.ServerHost
	port := env.ServerPort
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return err
	}

	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    env.ClientConnectionTTL,
			Timeout: 20 * time.Second,
		}),
	}
	if mz, ok := env.GrpcMaxReceiveSize.AsInt64(); ok {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}
	svc := server.New(ctx, opts...)
	s.RegisterServers(svc)
	clog.Debugf(ctx, "Serving client connections on %s using idle TTL %s", l.Addr(), env.ClientConnectionTTL)
	return server.Serve(ctx, svc, l)
}

func (s *service) RegisterServers(grpcHandler *grpc.Server) {
	rpc.RegisterManagerServer(grpcHandler, s)
	grpc_health_v1.RegisterHealthServer(grpcHandler, &HealthChecker{})
}

func (s *service) runUpdateTrafficManagerConfigMapLoop(ctx context.Context) error {
	// Loop updating the agentState every 2mins
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			clog.Tracef(ctx, "runUpdateTrafficManagerConfigMapLoop ticked, need to update configMap: %v", s.tmConfigMapUpdated.Load())
			if s.tmConfigMapUpdated.Load() {
				err := s.updateTrafficManagerConfigMap(ctx)
				if err != nil {
					clog.Errorf(ctx, "error in updating traffic manager config map, err: %v", err)
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}
