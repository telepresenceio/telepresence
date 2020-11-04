package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/ambassador/pkg/dexec"
	"github.com/datawire/ambassador/pkg/dlog"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/datawire/telepresence2/pkg/manager"
	"github.com/datawire/telepresence2/pkg/rpc"
	"github.com/datawire/telepresence2/pkg/version"
)

func manager_main() {
	// Set up context with logger
	dlog.SetFallbackLogger(makeBaseLogger())
	g, ctx := errgroup.WithContext(dlog.WithField(context.Background(), "MAIN", "main"))

	if version.Version == "" {
		version.Version = "(devel)"
	}

	dlog.Infof(ctx, "Traffic Manager %s [pid:%d]", version.Version, os.Getpid())

	// Handle shutdown
	g.Go(func() error {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigs:
			dlog.Errorf(ctx, "Shutting down due to signal %v", sig)
			return fmt.Errorf("received signal %v", sig)
		case <-ctx.Done():
			return nil
		}
	})

	// Run sshd
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "sshd")
		cmd := dexec.CommandContext(ctx, "/usr/sbin/sshd", "-De", "-p", "8022")

		// Avoid starting sshd while running locally for debugging. Launch sleep
		// instead so that the launch and kill code is tested in development.
		if os.Getenv("USER") != "" {
			dlog.Info(ctx, "Not starting sshd ($USER is set)")
			cmd = dexec.CommandContext(ctx, "sleep", "1000000")
		}

		if err := cmd.Start(); err != nil {
			return err
		}

		// If sshd quits, all port forwarding will cease to function. Call
		// Wait() and treat any exit as fatal.
		g.Go(func() error {
			err := cmd.Wait()
			if err != nil {
				return errors.Wrap(err, "sshd failed")
			}
			return errors.New("sshd finished: exit status 0")
		})

		<-ctx.Done()

		dlog.Debug(ctx, "sshd stopping...")

		if err := cmd.Process.Kill(); err != nil {
			dlog.Debugf(ctx, "kill sshd: %+v", err)
		}

		return nil
	})

	// Serve gRPC
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "server")

		host := os.Getenv("SERVER_HOST")
		port := os.Getenv("SERVER_PORT")
		if port == "" {
			port = "8081"
		}
		address := host + ":" + port

		lis, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}

		dlog.Infof(ctx, "Traffic Manager listening on %q", address)

		server := grpc.NewServer()
		rpc.RegisterManagerServer(server, manager.NewManager(ctx))
		grpc_health_v1.RegisterHealthServer(server, &HealthChecker{})

		g.Go(func() error {
			return server.Serve(lis)
		})

		<-ctx.Done()

		dlog.Debug(ctx, "Traffic Manager stopping...")
		server.Stop()
		lis.Close()

		return nil
	})

	// Serve HTTP
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "httpd")
		server := &http.Server{
			Addr:        ":8000", // FIXME configurable?
			ErrorLog:    dlog.StdLogger(ctx, dlog.LogLevelError),
			BaseContext: func(_ net.Listener) context.Context { return ctx },
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, "Hello World from: %s\n", r.URL.Path)
			}),
		}

		g.Go(server.ListenAndServe)

		<-ctx.Done()

		dlog.Debug(ctx, "Web server stopping...")
		server.Close()
		return nil
	})

	// Wait for exit
	if err := g.Wait(); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}
