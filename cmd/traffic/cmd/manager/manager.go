package manager

import (
	"context"
	"fmt"
	"net"
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
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var (
	DisplayName                   = "OSS Traffic Manager"               //nolint:gochecknoglobals // extension point
	NewServiceFunc                = NewService                          //nolint:gochecknoglobals // extension point
	WithAgentImageRetrieverFunc   = managerutil.WithAgentImageRetriever //nolint:gochecknoglobals // extension point
	IncrementInterceptCounterFunc = func(metric *prometheus.CounterVec, client, installId string, spec *rpc.InterceptSpec) {
		if metric != nil {
			labels := prometheus.Labels{
				"client":         client,
				"install_id":     installId,
				"intercept_type": "global",
			}

			metric.With(labels).Inc()
		}
	}
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

func MainWithEnv(ctx context.Context) (err error) {
	debug.SetTraceback("single")
	defer runtime.RecoverFromPanic(&err)

	dlog.Infof(ctx, "%s %s [uid:%d,gid:%d]", DisplayName, version.Version, os.Getuid(), os.Getgid())

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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, ki, ari)

	configWatcher := config.NewWatcher(env.ManagerNamespace)
	go func() {
		if err := configWatcher.Run(ctx); err != nil {
			dlog.Error(ctx, err)
		}
		cancel()
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
		dlog.Debug(ctx, "Using cluster wide informers")
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
	ctx = mutator.WithMap(ctx, mutator.Load(ctx))

	if mgrFactory {
		f := informer.GetK8sFactory(ctx, env.ManagerNamespace)
		f.Start(ctx.Done())
		f.WaitForCacheSync(ctx.Done())
	}

	mgr, g, err := NewServiceFunc(ctx, configWatcher)
	if err != nil {
		return fmt.Errorf("unable to initialize traffic manager: %w", err)
	}

	// Serve HTTP (including gRPC)
	g.Go("httpd", mgr.serveHTTP)
	g.Go("config", namespaces.Listen)
	g.Go("prometheus", mgr.servePrometheus)

	if managerutil.AgentInjectorEnabled(ctx) {
		g.Go("agent-injector", func(ctx context.Context) error {
			if managerutil.GetAgentImageRetriever(ctx) == nil {
				return nil
			}
			return mutator.ServeMutator(ctx, injectorCertGetter)
		})
	}

	g.Go("session-gc", mgr.runSessionGCLoop)

	if managerutil.GetEnv(ctx).AgentMaxIdleTime != 0 {
		// only start the configmap updater if we set the agent max idle time, as we need to persist the latest agent state to the config map
		//  otherwise everything else is passively synced which is ok if we don't need to clean up idle agents
		g.Go("configmap-updater", mgr.runUpdateTrafficManagerConfigMapLoop)
	}

	// Wait for exit
	return g.Wait()
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

func IncrementCounter(metric *prometheus.CounterVec, client, installId string) {
	if metric != nil {
		metric.With(prometheus.Labels{"client": client, "install_id": installId}).Inc()
	}
}

func SetGauge(metric *prometheus.GaugeVec, client, installId string, workload *string, value float64) {
	if metric != nil {
		labels := prometheus.Labels{
			"client":     client,
			"install_id": installId,
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
		dlog.Info(ctx, "Prometheus metrics server not started")
		return nil
	}
	newGaugeFunc("agent_count", "Number of connected traffic agents", s.state.CountAgents)
	newGaugeFunc("client_count", "Number of connected clients", s.state.CountClients)
	newGaugeFunc("active_intercept_count", "Number of active intercepts", s.state.CountIntercepts)
	newGaugeFunc("session_count", "Number of sessions", s.state.CountSessions)
	newGaugeFunc("tunnel_count", "Number of tunnels", s.state.CountTunnels)
	newCounterFunc("tunnel_ingress_bytes", "Number of bytes tunneled from clients", s.state.CountTunnelIngress)
	newCounterFunc("tunnel_egress_bytes", "Number bytes tunneled to clients", s.state.CountTunnelEgress)

	newGaugeFunc("active_http_request_count", "Number of currently served http requests", func() int {
		return int(atomic.LoadInt32(&s.activeHttpRequests))
	})

	newGaugeFunc("active_grpc_request_count", "Number of currently served gRPC requests", func() int {
		return int(atomic.LoadInt32(&s.activeGrpcRequests))
	})

	labels := []string{"client", "install_id"}
	s.state.SetPrometheusMetrics(
		newCounterVecFunc("connect_count", "The total number of connects by user", labels),
		newGaugeVecFunc("connect_active_status", "Flag to indicate when a connect is active. 1 for active, 0 for not active.", labels),
		newCounterVecFunc("intercept_count", "The total number of intercepts by user", append(labels, "intercept_type")),
		newGaugeVecFunc("intercept_active_status",
			"Flag to indicate when an intercept is active. 1 for active, 0 for not active.", append(labels, "workload")),
	)

	s.state.SetAllClientSessionsFinalizer(func(client *state.ClientSession) {
		SetGauge(s.state.GetConnectActiveStatus(), client.Name, client.InstallId, nil, 0)
	})

	s.state.SetAllInterceptsFinalizer(func(client *state.ClientSession, workload *string) {
		SetGauge(s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, workload, 0)
	})

	lg := dlog.StdLogger(ctx, dlog.MaxLogLevel(ctx))
	lg.SetPrefix(fmt.Sprintf("prometheus:%d", env.PrometheusPort))
	sc := &dhttp.ServerConfig{
		Handler:  promhttp.Handler(),
		ErrorLog: lg,
	}
	dlog.Infof(ctx, "Prometheus metrics server started on port: %d", env.PrometheusPort)
	defer dlog.Info(ctx, "Prometheus metrics server stopped")
	return sc.ListenAndServe(ctx, iputil.JoinHostPort(env.ServerHost, env.PrometheusPort))
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
	if mz, ok := env.MaxReceiveSize.AsInt64(); ok {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}
	svc := server.New(ctx, opts...)
	s.self.RegisterServers(svc)
	dlog.Debugf(ctx, "Serving client connections using idle TTL %s", env.ClientConnectionTTL)
	return server.Serve(ctx, svc, l)
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

func (s *service) runUpdateTrafficManagerConfigMapLoop(ctx context.Context) error {
	// Loop updating the agentState every 2mins
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dlog.Tracef(ctx, "runUpdateTrafficManagerConfigMapLoop ticked, need to update configMap: %v", s.tmConfigMapUpdated.Load())
			if s.tmConfigMapUpdated.Load() {
				err := s.updateTrafficManagerConfigMap(ctx)
				if err != nil {
					dlog.Errorf(ctx, "error in updating traffic manager config map, err: %v", err)
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}
