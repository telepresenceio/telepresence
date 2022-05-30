package manager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Main starts up the traffic manager and blocks until it ends
func Main(ctx context.Context, _ ...string) error {
	dlog.Infof(ctx, "Traffic Manager %s [pid:%d]", version.Version, os.Getpid())

	ctx, err := managerutil.LoadEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to LoadEnv: %w", err)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("unable to get the Kubernetes InClusterConfig: %w", err)
	}
	ki, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("unable to create the Kubernetes Interface from InClusterConfig: %w", err)
	}
	ctx = k8sapi.WithK8sInterface(ctx, ki)
	mgr, ctx, err := NewManager(ctx)
	if err != nil {
		return fmt.Errorf("unable to initialize traffic manager: %w", err)
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	// Serve HTTP (including gRPC)
	g.Go("httpd", mgr.serveHTTP)

	g.Go("agent-injector", mutator.ServeMutator)

	g.Go("session-gc", mgr.runSessionGCLoop)

	// Wait for exit
	return g.Wait()
}

func (m *Manager) serveHTTP(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	host := env.ServerHost
	port := env.ServerPort
	var opts []grpc.ServerOption
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
				grpcHandler.ServeHTTP(w, r)
			} else {
				httpHandler.ServeHTTP(w, r)
			}
		}),
	}

	rpc.RegisterManagerServer(grpcHandler, m)
	grpc_health_v1.RegisterHealthServer(grpcHandler, &HealthChecker{})

	return sc.ListenAndServe(ctx, host+":"+port)
}

func (m *Manager) runSessionGCLoop(ctx context.Context) error {
	// Loop calling Expire
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.expire(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}
