package manager

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/watchable"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Manager %s [pid:%d]", version.Version, os.Getpid())

	ctx, err := managerutil.LoadEnv(ctx)
	if err != nil {
		return err
	}

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})
	mgr := NewManager(ctx)

	// Serve HTTP (including gRPC)
	g.Go("httpd", func(ctx context.Context) error {
		env := managerutil.GetEnv(ctx)
		host := env.ServerHost
		port := env.ServerPort

		grpcHandler := grpc.NewServer()
		httpHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Hello World from: %s\n", r.URL.Path)
		}))
		server := &http.Server{
			Addr:     host + ":" + port,
			ErrorLog: dlog.StdLogger(ctx, dlog.LogLevelError),
			Handler: h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
					grpcHandler.ServeHTTP(w, r)
				} else {
					httpHandler.ServeHTTP(w, r)
				}
			}), &http2.Server{}),
		}

		rpc.RegisterManagerServer(grpcHandler, mgr)
		grpc_health_v1.RegisterHealthServer(grpcHandler, &HealthChecker{})

		return dutil.ListenAndServeHTTPWithContext(ctx, server)
	})

	g.Go("agent-injector", mutator.ServeMutator)

	g.Go("intercept-gc", func(ctx context.Context) error {
		// Loop calling Expire
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				mgr.expire()
			case <-ctx.Done():
				return nil
			}
		}
	})

	// This goroutine is responsible for informing System A of intercepts (and
	// relevant metadata like domains) that have been garbage collected. This
	// ensures System A doesn't list preview URLs + intercepts that no longer
	// exist.
	g.Go("systema-gc", func(ctx context.Context) error {
		for snapshot := range mgr.state.WatchIntercepts(ctx, nil) {
			for _, update := range snapshot.Updates {
				// Since all intercepts with a domain require a login, we can use
				// presence of the ApiKey in the interceptInfo to determine all
				// intercepts that we need to inform System A of their deletion
				if update.Delete && update.Value.ApiKey != "" {
					if sa, err := mgr.systema.Get(); err != nil {
						dlog.Errorln(ctx, "systema: acquire connection:", err)
					} else {
						// First we remove the PreviewDomain if it exists
						if update.Value.PreviewDomain != "" {
							err = mgr.reapDomain(ctx, sa, update)
							if err != nil {
								dlog.Errorln(ctx, "systema: remove domain:", err)
							}
						}
						// Now we inform SystemA of the intercepts removal
						dlog.Debugf(ctx, "systema: remove intercept: %q", update.Value.Id)
						err = mgr.reapIntercept(ctx, sa, update)
						if err != nil {
							dlog.Errorln(ctx, "systema: remove intercept:", err)
						}

						// Release the connection we got to delete the domain + intercept
						if err := mgr.systema.Done(); err != nil {
							dlog.Errorln(ctx, "systema: release management connection:", err)
						}
					}
					// Release the refcount on the proxy connection
					if err := mgr.systema.Done(); err != nil {
						dlog.Errorln(ctx, "systema: release proxy connection:", err)
					}
				}
			}
		}
		return nil
	})

	// Wait for exit
	return g.Wait()
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
