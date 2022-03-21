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
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
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

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})
	mgr := NewManager(ctx)

	// Serve HTTP (including gRPC)
	g.Go("httpd", mgr.serveHTTP)

	g.Go("agent-injector", mutator.ServeMutator)

	g.Go("intercept-gc", mgr.runInterceptGCLoop)

	// This goroutine is responsible for informing System A of intercepts (and
	// relevant metadata like domains) that have been garbage collected. This
	// ensures System A doesn't list preview URLs + intercepts that no longer
	// exist.
	g.Go("systema-gc", mgr.runSystemAGCLoop)

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

func (m *Manager) runInterceptGCLoop(ctx context.Context) error {
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

func (m *Manager) runSystemAGCLoop(ctx context.Context) error {
	for snapshot := range m.state.WatchIntercepts(ctx, nil) {
		for _, update := range snapshot.Updates {
			// Since all intercepts with a domain require a login, we can use
			// presence of the ApiKey in the interceptInfo to determine all
			// intercepts that we need to inform System A of their deletion
			if update.Delete && update.Value.ApiKey != "" {
				if sa, err := m.systema.Get(); err != nil {
					dlog.Errorln(ctx, "systema: acquire connection:", err)
				} else {
					// First we remove the PreviewDomain if it exists
					if update.Value.PreviewDomain != "" {
						err = m.reapDomain(ctx, sa, update)
						if err != nil {
							dlog.Errorln(ctx, "systema: remove domain:", err)
						}
					}
					// Now we inform SystemA of the intercepts removal
					dlog.Debugf(ctx, "systema: remove intercept: %q", update.Value.Id)
					err = m.reapIntercept(ctx, sa, update)
					if err != nil {
						dlog.Errorln(ctx, "systema: remove intercept:", err)
					}

					// Release the connection we got to delete the domain + intercept
					if err := m.systema.Done(); err != nil {
						dlog.Errorln(ctx, "systema: release management connection:", err)
					}
				}
			}
		}
	}
	return nil
}

// reapDomain informs SystemA that an intercept with a domain has been garbage collected
func (m *Manager) reapDomain(ctx context.Context, sa systema.SystemACRUDClient, interceptUpdate watchable.InterceptMapUpdate) error {
	// we only reapDomains for intercepts that have been deleted
	if !interceptUpdate.Delete {
		return fmt.Errorf("%s is not being deleted, so the domain was not reaped", interceptUpdate.Value.Id)
	}
	dlog.Debugf(ctx, "systema: removing domain: %q", interceptUpdate.Value.PreviewDomain)
	_, err := sa.RemoveDomain(ctx, &systema.RemoveDomainRequest{
		Domain: interceptUpdate.Value.PreviewDomain,
	})

	if err != nil {
		return err
	}
	return nil
}

// reapIntercept informs SystemA that an intercept has been garbage collected
func (m *Manager) reapIntercept(ctx context.Context, sa systema.SystemACRUDClient, interceptUpdate watchable.InterceptMapUpdate) error {
	// we only reapIntercept for intercepts that have been deleted
	if !interceptUpdate.Delete {
		return fmt.Errorf("%s is not being deleted, so the intercept was not reaped", interceptUpdate.Value.Id)
	}
	dlog.Debugf(ctx, "systema: remove intercept: %q", interceptUpdate.Value.Id)
	_, err := sa.RemoveIntercept(ctx, &systema.InterceptRemoval{
		InterceptId: interceptUpdate.Value.Id,
	})

	// We remove the APIKey whether or not the RemoveIntercept call was successful, so
	// let's do that before we check the error.
	if wasRemoved := m.state.RemoveInterceptAPIKey(interceptUpdate.Value.Id); !wasRemoved {
		dlog.Debugf(ctx, "Intercept ID %s had no APIKey", interceptUpdate.Value.Id)
	}

	if err != nil {
		return err
	}
	return nil
}
