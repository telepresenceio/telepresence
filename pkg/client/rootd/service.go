package rootd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
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
	titleName   = "Daemon"
	pprofFlag   = "pprof"
	logfileFlag = "logfile"
)

func help() string {
	return `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence rootd <config dir> <path to gRPC socket>

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(filelocation.AppUserLogDir(context.Background()), "daemon.log") + `
to troubleshoot problems.
`
}

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
}

func newService(cfg client.Config) *service {
	s := &service{
		timedLogLevel:  log.NewTimedLevel(cfg.LogLevels().RootDaemon.String(), log.SetLevel),
		sessionRunning: make(chan struct{}),
	}
	close(s.sessionRunning)
	return s
}

// Command returns the telepresence sub-command "rootd".
func Command(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:    client.RootDaemonName + " <config dir> <path to gRPC socket>",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		Long:   help(),
		RunE:   run,
	}
	flags := cmd.Flags()
	flags.Uint16(pprofFlag, 0, "start pprof server on the given port")
	flags.String(logfileFlag, filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log"),
		`Log file to write to { <path to a file> | "stdout" | "stderr" | "-" (same as "stderr") }`)
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
	c, cancel := context.WithCancel(c)
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

	configDir := args[0]
	rootDaemonPath := args[1]
	err := global.InitConfig(cmd)
	if err != nil {
		return err
	}

	c := cmd.Context()

	// Spoof the AppUserLogDir and AppUserConfigDir so that they return the original user's
	// directories rather than directories for the root user.
	c = filelocation.WithAppUserConfigDir(c, configDir)

	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)
	flags := cmd.Flags()
	if pprofPort, _ := flags.GetUint16(pprofFlag); pprofPort > 0 {
		go func() {
			if err := pprof.PprofServer(c, pprofPort); err != nil {
				dlog.Error(c, err)
			}
		}()
	}
	c = dgroup.WithGoroutineName(c, "/"+client.RootDaemonName)
	logFile := flags.Lookup(logfileFlag).Value.String()
	c, err = logging.InitContext(c, logFile, cfg.LogLevels().RootDaemon, logging.RotateDaily, true)
	if err != nil {
		return err
	}

	dlog.Debug(c, shellquote.ShellString(os.Args[0], os.Args[1:]))

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence Root Daemon %s starting...", client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Listen on domain unix domain socket. The listener must be opened before other tasks because
	// the CLI client will only wait for a short period of time for the socket to appear before it
	// gives up.
	grpcListener, err := socket.Listen(c, client.RootDaemonName, rootDaemonPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = socket.Remove(grpcListener)
	}()
	dlog.Debug(c, "Listener opened")

	d := newService(cfg)
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
