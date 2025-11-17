package rootd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/pprof"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	titleName   = "Root Daemon"
	pprofFlag   = "pprof"
	configFlag  = "config"
	logfileFlag = "logfile"
	socketFlag  = "socket"
	managedFlag = "managed"
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
	sessionCancel context.CancelFunc

	// sessionRunning is closed when the session is done running.
	sessionRunning chan struct{}
	managed        bool
}

func newService(cfg client.Config, managed bool) *service {
	s := &service{
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels().RootDaemon.String(), log.SetLevel),
		sessionRunning: make(chan struct{}),
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
	flags.String(configFlag, "", `Path to the Telepresence configuration file`)
	flags.String(socketFlag, "", `Path to gRPC socket`)
	flags.Bool(managedFlag, false, "The daemon is managed by the system and will disconnect, but not exit, when it receives RPC calls to Quit")
	return cmd
}

func (s *service) configReload(c context.Context) error {
	return client.WatchConfig(c, func(c context.Context) error {
		client.ReloadDaemonLogLevel(c)
		return nil
	})
}

func (s *service) serveGrpc(c context.Context, l net.Listener) error {
	var opts []grpc.ServerOption
	cfg := client.GetConfig(c)
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
	}

	var cancel context.CancelFunc
	if s.managed {
		// This essentially makes the quit() function wait until the session is done but otherwise do nothing.
		cancel = func() {}
	} else {
		c, cancel = context.WithCancel(c)
	}
	s.Context = c
	s.quit = func() {
		cancel()
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
		return fmt.Errorf("telepresence %s must run with elevated privileges", client.RootDaemonName)
	}

	flags := cmd.Flags()

	var configFile string
	cfgFlag := flags.Lookup(configFlag)
	if cfgFlag.Changed {
		configFile = cfgFlag.Value.String()
	}
	if configFile == "" {
		return fmt.Errorf("must specify %s", configFlag)
	}

	c := client.WithConfigFile(cmd.Context(), configFile)
	c = filelocation.WithAppUserConfigDir(c, filepath.Dir(configFile))
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)

	sockFlag := flags.Lookup(socketFlag)
	if !sockFlag.Changed {
		return fmt.Errorf("must specify %s", socketFlag)
	}
	rootDaemonPath := sockFlag.Value.String()
	nqFlag := flags.Lookup(managedFlag)

	var managed bool
	if nqFlag.Changed {
		managed, _ = strconv.ParseBool(nqFlag.Value.String())
	}

	if pprofPort, _ := flags.GetUint16(pprofFlag); pprofPort > 0 {
		go func() {
			if err := pprof.PprofServer(c, pprofPort); err != nil {
				dlog.Error(c, err)
			}
		}()
	}
	logFile := flags.Lookup(logfileFlag).Value.String()
	c, err = logging.InitContext(c, logFile, cfg.LogLevels().RootDaemon, logging.RotateDaily, true)
	if err != nil {
		return err
	}

	c = dgroup.WithGoroutineName(c, "/"+client.RootDaemonName)
	dlog.Debug(c, shellquote.ShellString(os.Args[0], os.Args[1:]))

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", titleName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Listen on domain unix domain socket. The listener must be opened before other tasks because
	// the CLI client will only wait for a short period of time for the socket to appear before it
	// gives up.
	grpcListener, err := socket.Listen(c, client.RootDaemonName, rootDaemonPath)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			var conn *grpc.ClientConn
			conn, err = socket.Dial(c, rootDaemonPath, false)
			if err == nil {
				var v *common.VersionInfo
				v, err = rpc.NewDaemonClient(conn).Version(c, &empty.Empty{})
				conn.Close()
				if err == nil {
					return fmt.Errorf("telepresence %s, version %s, is already running", titleName, v.Version)
				}
			}
			dlog.Warnf(c, "Socket %q exists but is not responding so the %s was terminated ungracefully", rootDaemonPath, client.RootDaemonName)
			dlog.Warnf(c, "Will remove and recreate %q", rootDaemonPath)
			err = os.Remove(rootDaemonPath)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("failed to remove %q: %w", rootDaemonPath, err)
			}
			grpcListener, err = socket.Listen(c, client.RootDaemonName, rootDaemonPath)
		}
		if err != nil {
			return fmt.Errorf("failed to listen on %q: %w", rootDaemonPath, err)
		}
	}
	dlog.Debug(c, "Listener opened")

	d := newService(cfg, managed)
	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, client.RootDaemonName); err != nil {
		return err
	}
	vif.InitLogger(c)

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  5 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
		IgnoreSignalError:    true,
	})

	// Add a reload function that triggers on create and write of the config.yml file.
	g.Go("config-reload", d.configReload)
	g.Go("server-grpc", func(c context.Context) error { return d.serveGrpc(c, grpcListener) })
	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}
