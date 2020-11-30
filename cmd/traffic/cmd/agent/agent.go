package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sethvargo/go-envconfig"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
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

func Main(ctx context.Context, args ...string) error {
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
		return err
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

	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		EnableSignalHandling: true,
	})

	// Manage the mechanism
	mechSubprocessDisabled := true
	g.Go("mech", func(ctx context.Context) error {
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

			err := cmd.Run()
			dlog.Infof(ctx, "mechanism terminated: %+v", err)

			if ctx.Err() != nil {
				return nil
			}
		}
	})

	var forwarder *Forwarder

	// Manage the forwarder
	g.Go("forward", func(ctx context.Context) error {
		lisAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AgentPort))
		if err != nil {
			return err
		}

		forwarder = NewForwarder(lisAddr)

		return forwarder.Serve(ctx, "", config.AppPort)
	})

	// Talk to the Traffic Manager
	g.Go("client", func(ctx context.Context) error {
		gRPCAddress := fmt.Sprintf("%s:%v", config.ManagerHost, config.ManagerPort)

		// Don't reconnect more than once every five seconds
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		state := NewState(forwarder, config.ManagerHost)

		for {
			if err := TalkToManager(ctx, gRPCAddress, info, state); err != nil {
				dlog.Info(ctx, err)
			}

			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	})

	// Wait for exit
	return g.Wait()
}
