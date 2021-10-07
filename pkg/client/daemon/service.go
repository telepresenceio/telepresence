package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const ProcessName = "daemon"
const titleName = "Daemon"

var help = `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence service

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), ProcessName+".log") + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Daemon
type service struct {
	rpc.UnsafeDaemonServer
	hClient       *http.Client
	outbound      *outbound
	cancel        context.CancelFunc
	timedLogLevel log.TimedLevel

	scoutClient *scout.Scout           // don't use this directly; use the 'scout' chan instead
	scout       chan scout.ScoutReport // any-of-scoutUsers -> background-metriton
}

// Command returns the telepresence sub-command "daemon-foreground"
func Command() *cobra.Command {
	return &cobra.Command{
		Use:    ProcessName + "-foreground <logging dir> <config dir> <dns>",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(3),
		Hidden: true,
		Long:   help,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), args[0], args[1], args[2])
		},
	}
}

func (d *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (d *service) Status(_ context.Context, _ *empty.Empty) (*rpc.DaemonStatus, error) {
	r := &rpc.DaemonStatus{
		OutboundConfig: d.outbound.getInfo(),
	}
	return r, nil
}

func (d *service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(ctx, "Received gRPC Quit")
	d.cancel()
	return &empty.Empty{}, nil
}

func (d *service) SetDnsSearchPath(ctx context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	d.outbound.setSearchPath(ctx, paths.Paths, paths.Namespaces)
	return &empty.Empty{}, nil
}

func (d *service) SetOutboundInfo(ctx context.Context, info *rpc.OutboundInfo) (*empty.Empty, error) {
	return &empty.Empty{}, d.outbound.setInfo(ctx, info)
}

func (d *service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (*empty.Empty, error) {
	duration := time.Duration(0)
	if request.Duration != nil {
		duration = request.Duration.AsDuration()
	}
	return &empty.Empty{}, logging.SetAndStoreTimedLevel(ctx, d.timedLogLevel, request.LogLevel, duration, ProcessName)
}

// run is the main function when executing as the daemon
func run(c context.Context, loggingDir, configDir, dns string) error {
	if !proc.IsAdmin() {
		return fmt.Errorf("telepresence %s must run with elevated privileges", ProcessName)
	}

	// Spoof the AppUserLogDir and AppUserConfigDir so that they return the original user's
	// directories rather than directories for the root user.
	c = filelocation.WithAppUserLogDir(c, loggingDir)
	c = filelocation.WithAppUserConfigDir(c, configDir)
	cfg, err := client.LoadConfig(c)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	c = client.WithConfig(c, cfg)

	c = dgroup.WithGoroutineName(c, "/"+ProcessName)
	c, err = logging.InitContext(c, ProcessName)
	if err != nil {
		return err
	}

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", ProcessName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// Listen on domain unix domain socket or windows named pipe. The listener must be opened
	// before other tasks because the CLI client will only wait for a short period of time for
	// the socket/pipe to appear before it gives up.
	grpcListener, err := client.ListenSocket(c, ProcessName, client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.RemoveSocket(grpcListener)
	}()
	dlog.Debug(c, "Listener opened")

	d := &service{
		hClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				// #nosec G402
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				Proxy:           nil,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 1 * time.Second,
				}).DialContext,
				DisableKeepAlives: true,
			},
		},
		scoutClient:   scout.NewScout(c, "daemon"),
		scout:         make(chan scout.ScoutReport, 25),
		timedLogLevel: log.NewTimedLevel(cfg.LogLevels.RootDaemon.String(), log.SetLevel),
	}
	if err = logging.LoadTimedLevelFromCache(c, d.timedLogLevel, ProcessName); err != nil {
		return err
	}

	d.outbound, err = newOutbound(c, dns, false, d.scout)
	if err != nil {
		return err
	}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// The d.cancel will start a "quit" go-routine that will cause the group to initiate a shutdown when it returns.
	d.cancel = func() { g.Go(ProcessName+"-quit", d.quitAll) }

	// server-dns runs a local DNS server that resolves *.cluster.local names.  Exactly where it
	// listens varies by platform.
	var scoutUsers sync.WaitGroup // how many of the goroutines might write to d.scout; any that use d.outbound are liable to do this, as it's passed into it.
	scoutUsers.Add(1)
	g.Go("server-dns", func(ctx context.Context) error {
		defer scoutUsers.Done()
		select {
		case <-ctx.Done():
			return nil
		case <-d.outbound.router.configured():
			return d.outbound.dnsServerWorker(ctx)
		}
	})

	// The 'Update' gRPC (below) call puts updates in to a work queue;
	// server-router is the worker process starts the router and continuously configures
	// it based on what is read from that work queue.
	scoutUsers.Add(1)
	g.Go("server-router", func(ctx context.Context) error {
		defer scoutUsers.Done()
		return d.outbound.router.run(ctx)
	})

	// server-grpc listens on /var/run/telepresence-daemon.socket and services gRPC requests
	// from the connector and from the CLI.
	scoutUsers.Add(1)
	g.Go("server-grpc", func(c context.Context) (err error) {
		defer func() {
			scoutUsers.Done()
			// Error recovery.
			if perr := derror.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
			}
		}()

		defer func() {
			if err != nil {
				dlog.Errorf(c, "gRPC server ended with: %v", err)
			} else {
				dlog.Debug(c, "gRPC server ended")
			}
		}()

		opts := []grpc.ServerOption{}
		if mxRecvSize := client.GetConfig(c).Grpc.MaxReceiveSize; mxRecvSize != nil {
			if mz, ok := mxRecvSize.AsInt64(); ok {
				opts = append(opts, grpc.MaxRecvMsgSize(int(mz)))
			}
		}
		svc := grpc.NewServer(opts...)
		rpc.RegisterDaemonServer(svc, d)

		sc := &dhttp.ServerConfig{
			Handler: svc,
		}
		dlog.Info(c, "gRPC server started")
		return sc.Serve(c, grpcListener)
	})

	// background-metriton is the goroutine that handles all telemetry reports, so that calls to
	// metriton don't block the functional goroutines.
	g.Go("background-metriton", func(c context.Context) error {
		for report := range d.scout {
			for k, v := range report.PersistentMetadata {
				d.scoutClient.SetMetadatum(k, v)
			}

			var metadata []scout.ScoutMeta
			for k, v := range report.Metadata {
				metadata = append(metadata, scout.ScoutMeta{
					Key:   k,
					Value: v,
				})
			}
			d.scoutClient.Report(c, report.Action, metadata...)
		}
		return nil
	})
	go func() {
		scoutUsers.Wait()
		close(d.scout)
	}()

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}

// quitAll shuts down the router and calls quitConnector
func (d *service) quitAll(c context.Context) error {
	d.outbound.router.stop(c)
	return d.quitConnector(c)
}

// quitConnector ensures that the connector quits gracefully.
func (d *service) quitConnector(c context.Context) error {
	exists, err := client.SocketExists(client.ConnectorSocketName)
	if err != nil {
		// connector socket problem, so nothing to shut down
		dlog.Errorf(c, "Daemon cannot quit connector: %v", err)
		return nil
	}
	if !exists {
		// no connector socket, so nothing to shut down
		return nil
	}

	// Send a "quit" message from here.
	dlog.Info(c, "Shutting down connector")
	c, cancel := context.WithTimeout(c, 500*time.Millisecond)
	defer cancel()
	conn, err := client.DialSocket(c, client.ConnectorSocketName)
	if err != nil {
		return nil
	}
	defer conn.Close()
	dlog.Debug(c, "Sending quit message to connector")
	_, _ = connector.NewConnectorClient(conn).Quit(c, &empty.Empty{})
	dlog.Debug(c, "Connector shutdown complete")
	time.Sleep(200 * time.Millisecond) // Give some time to receive final log messages from connector
	return nil
}
