package rootd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/pprof"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	titleName    = "Root Daemon"
	pprofFlag    = "pprof"
	cacheDirFlag = "cache"
	configFlag   = "config"
	logfileFlag  = "logfile"
	addressFlag  = "address"
	managedFlag  = "managed"
)

// service represents the state of the Telepresence Daemon.
type service struct {
	context.Context
	rpc.UnsafeDaemonServer
	quit          context.CancelFunc
	timedLogLevel log.TimedLevel

	// sessionLock protects the session, sessionCancel, and sessionRunning fields.
	sessionLock   sync.RWMutex
	session       *session
	sessionCancel func(error)

	// sessionRunning is closed when the session is done running.
	sessionRunning chan struct{}
	activity       chan time.Time
	managed        bool
}

func newService(cfg client.Config, managed bool) *service {
	s := &service{
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels().RootDaemon, clog.SetTreeLevel),
		sessionRunning: make(chan struct{}),
		activity:       make(chan time.Time, 10),
		managed:        managed,
	}
	close(s.sessionRunning)
	return s
}

// Command returns the telepresence sub-command "rootd".
func Command(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    client.RootDaemonName,
		Short:  "Launch Telepresence " + titleName,
		Args:   cobra.NoArgs,
		Hidden: true,
		Long:   `The Telepresence ` + titleName + ` is a long-lived background component that manages connections and network state.`,
		RunE:   run,
	}
	flags := cmd.Flags()
	flags.Uint16(pprofFlag, 0, "start pprof server on the given port")
	flags.String(logfileFlag, "", `Log file to write to { <path to a file> | "stdout" | "stderr" | "std" | "managed" }
"std" will cause the daemon to log informal messages to stdout and error messages to stderr
"managed" is like "std", but without timestamps and level tags for "error" or "info" messages`)
	flags.String(cacheDirFlag, "", `Path to the Telepresence cache directory`)
	flags.String(configFlag, "", `Path to the Telepresence configuration file`)
	flags.String(addressFlag, "", "TCP address to listen to")
	flags.Bool(managedFlag, false, "The daemon is managed by the system and will disconnect, but not exit, when it receives RPC calls to Quit")
	return cmd
}

func (s *service) configReload(c context.Context) error {
	return client.WatchConfig(c, func(c context.Context) error {
		client.ReloadLogLevel(c)
		return nil
	})
}

func (s *service) serveGrpc(c context.Context, groupCancel context.CancelFunc, l net.Listener) error {
	var opts []grpc.ServerOption
	cfg := client.GetConfig(c)
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}

	if s.managed {
		// This essentially makes the quit() function wait until the session is done but otherwise do nothing.
		groupCancel = func() {}
	}
	s.Context = c
	s.quit = func() {
		groupCancel()
		s.sessionLock.RLock()
		sessionRunning := s.sessionRunning
		s.sessionLock.RUnlock()
		<-sessionRunning
	}
	svc := server.New(s, opts...)
	rpc.RegisterDaemonServer(svc, s)
	return server.Serve(s, svc, l)
}

// run is the main function when executing as the daemon.
func run(cmd *cobra.Command, args []string) error {
	if !proc.IsAdmin() {
		msg := fmt.Sprintf("telepresence %s must run with elevated privileges", client.RootDaemonName)
		ioutil.Println(os.Stderr, msg)
		return errors.New(msg)
	}
	return sigctx.DoWithSignalHandler(cmd.Context(), func(ctx context.Context) error {
		return internalRun(ctx, cmd.Flags())
	})
}

func internalRun(c context.Context, flags *pflag.FlagSet) error {
	cacheDir := flags.Lookup(cacheDirFlag).Value.String()
	if cacheDir != "" {
		c = filelocation.WithAppUserCacheDir(c, cacheDir)
	}
	var cfg client.Config
	var err error

	var configFile string
	cfgFlag := flags.Lookup(configFlag)
	if cfgFlag.Changed {
		configFile = cfgFlag.Value.String()
	}

	if configFile == "" {
		cfg = client.GetDefaultConfig()
	} else {
		c = client.WithConfigFile(c, configFile)
		c = filelocation.WithAppUserConfigDir(c, filepath.Dir(configFile))
		cfg, err = client.LoadConfig(c)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}
	c = client.WithConfig(c, cfg)

	addrFlag := flags.Lookup(addressFlag)
	if !addrFlag.Changed {
		return fmt.Errorf("must specify %s", addressFlag)
	}
	addrStr := addrFlag.Value.String()
	nqFlag := flags.Lookup(managedFlag)

	var managed bool
	if nqFlag.Changed {
		managed, _ = strconv.ParseBool(nqFlag.Value.String())
	}

	if pprofPort, _ := flags.GetUint16(pprofFlag); pprofPort > 0 {
		go func() {
			if err := pprof.PprofServer(c, pprofPort); err != nil {
				clog.Error(c, err)
			}
		}()
	}
	logFile := flags.Lookup(logfileFlag).Value.String()
	c, err = logging.InitContext(c, logFile, cfg.LogLevels().RootDaemon, logging.RotateDaily, true)
	if err != nil {
		return err
	}
	c = clog.WithGroup(c, client.RootDaemonName)

	clog.Debug(c, shellquote.ShellString(os.Args[0], os.Args[1:]))

	clog.Info(c, "---")
	clog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	clog.Infof(c, "PID is %d", os.Getpid())
	clog.Info(c, "")

	// Listen on domain unix domain socket. The listener must be opened before other tasks because
	// the CLI client will only wait for a short period of time for the socket to appear before it
	// gives up.
	lc := net.ListenConfig{}
	grpcListener, err := lc.Listen(c, "tcp", addrStr)
	if err != nil {
		return err
	}
	defer grpcListener.Close()

	daemonAddress := grpcListener.Addr().(interface{ AddrPort() netip.AddrPort }).AddrPort()
	clog.Debugf(c, "Listener opened on %s", grpcListener.Addr())

	d := newService(cfg, managed)
	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, client.RootDaemonName); err != nil {
		clog.Error(c, err)
		return err
	}
	vif.InitLogger(c)

	c, cancel := context.WithCancel(c)
	g := log.NewGroup(c)
	runAliveAndCancellation(g, daemonAddress.Port(), cancel, managed)

	// Add a reload function that triggers on create and write of the config.yml file.
	g.Go("config-reload", d.configReload)
	g.Go("server-grpc", func(c context.Context) error { return d.serveGrpc(c, cancel, grpcListener) })
	err = g.Wait()
	if err != nil {
		clog.Error(c, err)
	}
	return err
}

func runAliveAndCancellation(g log.Group, daemonPort uint16, cancel context.CancelFunc, managed bool) {
	g.Go("info-kicker", func(ctx context.Context) error {
		// Ensure that the daemon info file is kept recent. This tells clients that we're alive.
		il := daemon.NewRootInfoLoader(ctx, managed)
		if managed {
			info := &daemon.RootInfo{DaemonPort: daemonPort}
			err := il.SaveInfo(info, daemon.InfoFileName)
			if err != nil {
				return err
			}
		}
		return il.KeepInfoAlive(daemon.InfoFileName)
	})
	g.Go("info-watcher", func(ctx context.Context) error {
		// Cancel the session if the daemon info file is removed.
		il := daemon.NewRootInfoLoader(ctx, managed)
		return il.WatchInfos(func(ctx context.Context) error {
			ok, err := il.InfoExists(daemon.InfoFileName)
			if err == nil && !ok {
				clog.Warnf(ctx, "info-watcher cancels everything because daemon info %s does not exist", daemon.InfoFileName)
				cancel()
			}
			return err
		}, daemon.InfoFileName)
	})
}
