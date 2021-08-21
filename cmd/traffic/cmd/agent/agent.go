package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type Config struct {
	Name        string `env:"_TEL_AGENT_NAME,required"`
	Namespace   string `env:"_TEL_AGENT_NAMESPACE,default="`
	PodIP       string `env:"_TEL_AGENT_POD_IP,default="`
	AgentPort   int32  `env:"_TEL_AGENT_PORT,default=9900"`
	AppMounts   string `env:"_TEL_AGENT_APP_MOUNTS,default=/tel_app_mounts"`
	AppPort     int32  `env:"_TEL_AGENT_APP_PORT,required"`
	ManagerHost string `env:"_TEL_AGENT_MANAGER_HOST,default=traffic-manager"`
	ManagerPort int32  `env:"_TEL_AGENT_MANAGER_PORT,default=8081"`
}

var skipKeys = map[string]bool{
	// Keys found in the Config
	"_TEL_AGENT_NAME":         true,
	"_TEL_AGENT_NAMESPACE":    true,
	"_TEL_AGENT_POD_IP":       true,
	"_TEL_AGENT_PORT":         true,
	"_TEL_AGENT_APP_MOUNTS":   true,
	"_TEL_AGENT_APP_PORT":     true,
	"_TEL_AGENT_MANAGER_HOST": true,
	"_TEL_AGENT_MANAGER_PORT": true,

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
			if _, skip := skipKeys[k]; !skip {
				fullEnv[k] = pair[1]
			}
		}
	}
	for k, v := range appEnv {
		fullEnv[k] = v
	}
	return fullEnv
}

const tpMountsEnv = "TELEPRESENCE_MOUNTS"

func (cfg *Config) HasMounts(ctx context.Context, env map[string]string) bool {
	tpMounts := env[tpMountsEnv]
	if tpMounts != "" {
		dlog.Debugf(ctx, "agent mount paths: %s", tpMounts)
		return true
	}
	return false
}

// Add any token-rotating system secrets directories if they exist
// e.g. /var/run/secrets/kubernetes.io or /var/run/secrets/eks.amazonaws.com
func (cfg *Config) addSecretsMounts(ctx context.Context, env map[string]string) error {
	tpMounts := env[tpMountsEnv]

	// This will attempt to handle all the secrets dirs, but will return the first error we encountered.
	secretsDir, err := os.Open("/var/run/secrets")
	if err != nil {
		return err
	}
	fileInfo, err := secretsDir.Readdir(-1)
	if err != nil {
		return err
	}
	secretsDir.Close()
	for _, file := range fileInfo {
		// Directories found in /var/run/secrets get a symlink in appmounts
		if file.IsDir() {
			dirPath := filepath.Join("/var/run/secrets/", file.Name())
			dlog.Debugf(ctx, "checking agent secrets mount path: %s", dirPath)
			stat, err := os.Stat(dirPath)
			if err != nil {
				return err
			}
			if stat.IsDir() {
				appMountsPath := filepath.Join(cfg.AppMounts, dirPath)
				dlog.Debugf(ctx, "checking appmounts directory: %s", dirPath)
				// Make sure the path doesn't already exist
				_, err = os.Stat(appMountsPath)
				if err == nil {
					return fmt.Errorf("appmounts '%s' already exists", appMountsPath)
				}
				dlog.Debugf(ctx, "create appmounts directory: %s", appMountsPath)
				// Add a link to the kubernetes.io directory under {{.AppMounts}}/var/run/secrets
				err = os.MkdirAll(filepath.Dir(appMountsPath), 0700)
				if err != nil {
					return err
				}
				dlog.Debugf(ctx, "create appmounts symlink: %s %s", dirPath, appMountsPath)
				err = os.Symlink(dirPath, appMountsPath)
				if err != nil {
					return err
				}
				dlog.Infof(ctx, "new agent secrets mount path: %s", dirPath)
				if tpMounts == "" {
					tpMounts = dirPath
				} else {
					tpMounts += ":" + dirPath
				}
			}
		}
	}
	env[tpMountsEnv] = tpMounts
	return nil
}

func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Agent %s [pid:%d]", version.Version, os.Getpid())

	// Add defaults for development work
	user := os.Getenv("USER")
	if user != "" {
		dlog.Infof(ctx, "Launching in dev mode ($USER is set)")
		if os.Getenv("_TEL_AGENT_NAME") == "" {
			os.Setenv("_TEL_AGENT_NAME", "test-agent")
		}
		if os.Getenv("_TEL_AGENT_APP_PORT") == "" {
			os.Setenv("_TEL_AGENT_APP_PORT", "8080")
		}
	}

	// Handle configuration
	config := Config{}
	if err := envconfig.Process(ctx, &config); err != nil {
		return err
	}
	dlog.Infof(ctx, "%+v", config)

	info := &rpc.AgentInfo{
		Name:        config.Name,
		PodIp:       config.PodIP,
		Product:     "telepresence",
		Version:     version.Version,
		Environment: AppEnvironment(),
		Namespace:   config.Namespace,
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

	if err := config.addSecretsMounts(ctx, info.Environment); err != nil {
		dlog.Errorf(ctx, "There was a problem with agent mounts: %v", err)
	}

	sftpPortCh := make(chan int32)
	if config.HasMounts(ctx, info.Environment) && user == "" {
		g.Go("sftp-server", func(ctx context.Context) error {
			defer close(sftpPortCh)

			// start an sftp-server for remote sshfs mounts
			lc := net.ListenConfig{}
			l, err := lc.Listen(ctx, "tcp4", ":0")
			if err != nil {
				return err
			}

			// Accept doesn't actually return when the context is cancelled so
			// it's explicitly closed here.
			go func() {
				<-ctx.Done()
				_ = l.Close()
			}()

			_, sftpPort, err := iputil.SplitToIPPort(l.Addr())
			if err != nil {
				return err
			}
			sftpPortCh <- int32(sftpPort)

			for {
				conn, err := l.Accept()
				if err != nil {
					if ctx.Err() == nil {
						return fmt.Errorf("listener on sftp-server connection failed: %v", err)
					}
					return nil
				}
				go func() {
					dlog.Debugf(ctx, "Serving sshfs connection from %s", conn.RemoteAddr())
					err := dpipe.DPipe(ctx, conn, "/usr/lib/ssh/sftp-server")
					if err != nil {
						dlog.Error(ctx, err)
					}
				}()
			}
		})
	} else {
		close(sftpPortCh)
		dlog.Info(ctx, "Not starting sftp-server ($APP_MOUNTS is empty or $USER is set)")
	}

	forwarderChan := make(chan *forwarder.Forwarder)

	// Manage the forwarder
	g.Go("forward", func(ctx context.Context) error {
		ctx = connpool.WithPool(ctx, connpool.NewPool())
		lisAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", config.AgentPort))
		if err != nil {
			close(forwarderChan)
			return err
		}

		forwarder := forwarder.NewForwarder(lisAddr, "", config.AppPort)
		forwarderChan <- forwarder

		return forwarder.Serve(ctx)
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

		sftpPort := <-sftpPortCh
		state := NewState(forwarder, config.ManagerHost, config.Namespace, config.PodIP, sftpPort)

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
