package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/version"
)

func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Manager %s [pid:%d]", version.Version, os.Getpid())

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	// Run sshd
	g.Go("sshd", func(ctx context.Context) error {
		cmd := dexec.CommandContext(ctx, "/usr/sbin/sshd", "-De", "-p", "8022")

		// Avoid starting sshd while running locally for debugging. Launch sleep
		// instead so that the launch and kill code is tested in development.
		if os.Getenv("USER") != "" {
			dlog.Info(ctx, "Not starting sshd ($USER is set)")
			cmd = dexec.CommandContext(ctx, "sleep", "1000000")
		}

		return cmd.Run()
	})

	// Serve gRPC
	g.Go("grpcd", func(ctx context.Context) error {
		host := os.Getenv("SERVER_HOST")
		port := os.Getenv("SERVER_PORT")
		if port == "" {
			port = "8081"
		}

		grpcHandler := grpc.NewServer()
		httpHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "this socket only supports gRPC, not plain HTTP", http.StatusBadRequest)
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

		mgr := NewManager(ctx)
		rpc.RegisterManagerServer(grpcHandler, mgr)
		grpc_health_v1.RegisterHealthServer(grpcHandler, &HealthChecker{})

		g.Go("gc", func(ctx context.Context) error {
			// Loop calling Expire
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					mgr.Expire()
				case <-ctx.Done():
					return nil
				}
			}
		})

		return dutil.ListenAndServeHTTPWithContext(ctx, server)
	})

	// Serve HTTP
	g.Go("httpd", func(ctx context.Context) error {
		server := &http.Server{
			Addr:        ":8000", // FIXME configurable?
			ErrorLog:    dlog.StdLogger(ctx, dlog.LogLevelError),
			BaseContext: func(_ net.Listener) context.Context { return ctx },
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "Hello World from: %s\n", r.URL.Path)
			}),
		}

		return dutil.ListenAndServeHTTPWithContext(ctx, server)
	})

	// Wait for exit
	return g.Wait()
}
