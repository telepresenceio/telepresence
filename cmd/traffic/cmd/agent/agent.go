package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/pkg/version"
)

type Config struct {
	Name        string `env:"AGENT_NAME,required"`
	Namespace   string `env:"AGENT_NAMESPACE,required"`
	PodName     string `env:"AGENT_POD_NAME,required"`
	AgentPort   int32  `env:"AGENT_PORT,default=9900"`
	AppPort     int32  `env:"APP_PORT,required"`
	ManagerHost string `env:"MANAGER_HOST,default=traffic-manager"`
	ManagerPort int32  `env:"MANAGER_PORT,default=8081"`
}

var skipKeys = map[string]bool{
	// Keys found in the Config
	"AGENT_NAME":      true,
	"AGENT_NAMESPACE": true,
	"AGENT_POD_NAME":  true,
	"AGENT_PORT":      true,
	"APP_PORT":        true,
	"MANAGER_HOST":    true,
	"MANAGER_PORT":    true,

	// Keys that aren't useful when running on the local machine
	"HOME":     true,
	"PATH":     true,
	"HOSTNAME": true,
}

// AppEnvironment returns the environment visible to this agent together with environment variables
// explicitly declared for the app container and minus the environment variables provided by this
// config.
func AppEnvironment() map[string]string {
	osEnv := os.Environ()
	// Keep track of the "TEL_APP_"-prefixed variables separately at first, so that we can
	// ensure that they have higher precedence.
	appEnv := make(map[string]string)
	fullEnv := make(map[string]string, len(osEnv))
	for _, env := range osEnv {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			k := pair[0]
			if strings.HasPrefix(k, "TEL_APP_") {
				appEnv[k[len("TEL_APP_"):]] = pair[1]
			} else if _, skip := skipKeys[k]; !skip {
				fullEnv[k] = pair[1]
			}
		}
	}
	for k, v := range appEnv {
		fullEnv[k] = v
	}
	return fullEnv
}

func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Agent %s [pid:%d]", version.Version, os.Getpid())

	// Add defaults for development work
	user := os.Getenv("USER)")
	if user != "" {
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
		Name:        config.Name,
		Hostname:    hostname,
		Product:     "telepresence",
		Version:     version.Version,
		Environment: AppEnvironment(),
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

	sshPort := 0
	if user == "" {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return err
		}
		sshPort = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}

	// Run sshd
	g.Go("sshd", func(ctx context.Context) error {
		var cmd *dexec.Cmd

		// Avoid starting sshd while running locally for debugging. Launch sleep
		// instead so that the launch and kill code is tested in development.
		if user != "" {
			dlog.Info(ctx, "Not starting sshd ($USER is set)")
			cmd = dexec.CommandContext(ctx, "sleep", "1000000")
		} else {
			cmd = dexec.CommandContext(ctx, "/usr/sbin/sshd", "-De", "-p", strconv.Itoa(sshPort))
		}
		return cmd.Run()
	})

	forwarderChan := make(chan *Forwarder)

	// Manage the forwarder
	g.Go("forward", func(ctx context.Context) error {
		lisAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AgentPort))
		if err != nil {
			close(forwarderChan)
			return err
		}

		forwarder := NewForwarder(lisAddr)
		forwarderChan <- forwarder

		return forwarder.Serve(ctx, "", config.AppPort)
	})

	// Talk to the Traffic Manager
	g.Go("client", func(ctx context.Context) error {
		gRPCAddress := fmt.Sprintf("%s:%v", config.ManagerHost, config.ManagerPort)

		// Don't reconnect more than once every five seconds
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		forwarder := <-forwarderChan
		if forwarder == nil {
			return nil
		}

		state := NewState(forwarder, config.ManagerHost, config.PodName, int32(sshPort))

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
