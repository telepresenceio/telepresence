package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/ambassador/pkg/dlog"
	"github.com/sethvargo/go-envconfig"
	"golang.org/x/sync/errgroup"

	"github.com/datawire/telepresence2/pkg/version"
)

type Config struct {
	Name    string `env:"AGENT_NAME,required"`
	AppPort int    `env:"APP_PORT,required"`

	ManagerAddress   string `env:"MANAGER_ADDRESS,default=traffic-manager:8081"`
	SshHost          string `env:"SSH_HOST,default=traffic-manager"`
	SshPort          int    `env:"SSH_PORT,default=8022"`
	AgentPort        int    `env:"AGENT_PORT,default=9900"`
	DefaultMechanism string `env:"DEFAULT_MECHANISM,default=tcp"`
}

func agent_main() {
	// Set up context with logger
	dlog.SetFallbackLogger(makeBaseLogger())
	g, ctx := errgroup.WithContext(dlog.WithField(context.Background(), "MAIN", "main"))

	if version.Version == "" {
		version.Version = "(devel)"
	}

	dlog.Infof(ctx, "Traffic Agent %s [pid:%d]", version.Version, os.Getpid())

	// Add defaults for development work
	if os.Getenv("USER") != "" {
		dlog.Infof(ctx, "Launching in dev mode ($USER is set)")
		if os.Getenv("AGENT_NAME") == "" {
			os.Setenv("AGENT_NAME", "test-agent")
		}
		if os.Getenv("APP_PORT") == "" {
			os.Setenv("APP_PORT", "8080")
		}
	}

	// Handle configuration
	config := Config{}
	if err := envconfig.Process(ctx, &config); err != nil {
		dlog.Error(ctx, err)
		os.Exit(1)
	}
	dlog.Infof(ctx, "%+v", config)

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

	// Wait for exit
	if err := g.Wait(); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}
