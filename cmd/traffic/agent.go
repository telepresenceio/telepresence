package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sethvargo/go-envconfig"
	"golang.org/x/sync/errgroup"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/pkg/agent"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/version"
)

type Config struct {
	Name    string `env:"AGENT_NAME,required"`
	AppPort int32  `env:"APP_PORT,required"`

	AgentPort        int32  `env:"AGENT_PORT,default=9900"`
	DefaultMechanism string `env:"DEFAULT_MECHANISM,default=tcp"`
	ManagerHost      string `env:"MANAGER_HOST,default=traffic-manager"`
	ManagerPort      int32  `env:"MANAGER_PORT,default=8081"`
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

	hostname, err := os.Hostname()
	if err != nil {
		dlog.Infof(ctx, "hostname: %+v", err)
		hostname = fmt.Sprintf("unknown: %+v", err)
	}

	info := &rpc.AgentInfo{
		Name:     config.Name,
		Hostname: hostname,
		Product:  "telepresence",
		Version:  version.Version,
	}

	// Select initial mechanism
	mechanisms := []*rpc.AgentInfo_Mechanism{
		{
			Name:    "tcp",
			Product: "telepresence",
			Version: version.Version,
		},
	}
	info.Mechanisms = mechanisms

	// Manage the mechanism
	mechSubprocessDisabled := true
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "mech")

		envAdd := []string{
			fmt.Sprintf("AGENT_PORT=%v", config.AgentPort),
			fmt.Sprintf("APP_PORT=%v", config.AppPort),
			fmt.Sprintf("MECHANISM=%s", "tcp"), // FIXME
			fmt.Sprintf("MANAGER_HOST=%s", config.ManagerHost),
		}

		if mechSubprocessDisabled {
			return nil
		}

		for {
			// Launch/start the mechanism
			cmd := dexec.CommandContext(ctx, os.Args[0], "mech-tcp") // FIXME
			cmd.Env = append(os.Environ(), envAdd...)

			if err := cmd.Start(); err != nil {
				return err
			}

			mechQuit := make(chan error)
			go func() { mechQuit <- cmd.Wait() }()

			select {
			case err := <-mechQuit:
				dlog.Infof(ctx, "wait on mech: %+v", err)
				continue // launch new mechanism
			case <-ctx.Done():
				if err := cmd.Process.Kill(); err != nil {
					dlog.Debugf(ctx, "kill mech: %+v", err)
				}
				return nil
			}
		}
	})

	var forwarder *agent.Forwarder

	// Manage the forwarder
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "forward")

		lisAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AgentPort))
		if err != nil {
			return err
		}

		forwarder = agent.NewForwarder(lisAddr)

		return forwarder.Serve(ctx, "", config.AppPort)
	})

	// Talk to the Traffic Manager
	g.Go(func() error {
		ctx := dlog.WithField(ctx, "MAIN", "client")
		gRPCAddress := fmt.Sprintf("%s:%v", config.ManagerHost, config.ManagerPort)

		// Don't reconnect more than once every five seconds
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		state := agent.NewState(forwarder, config.ManagerHost)

		for {
			if err := agent.TalkToManager(ctx, gRPCAddress, info, state); err != nil {
				dlog.Info(ctx, err)
			}

			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	})

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
