package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const processName = "daemon"
const titleName = "Daemon"

var help = `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence service

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(func() string { dir, _ := filelocation.AppUserLogDir(context.Background()); return dir }(), processName+".log") + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Daemon
type service struct {
	rpc.UnsafeDaemonServer
	dns      string
	fallback string
	hClient  *http.Client
	outbound *outbound
	cancel   context.CancelFunc

	// callCtx is a hack for the gRPC server, since it doesn't let us pass it a Context.  It
	// should go away when we migrate to dhttp and Contexts can get passed around properly for
	// HTTP/2 requests.
	callCtx context.Context
}

// Command returns the telepresence sub-command "daemon-foreground"
func Command() *cobra.Command {
	return &cobra.Command{
		Use:    processName + "-foreground",
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
	r := &rpc.DaemonStatus{}
	r.Dns = d.dns
	r.Fallback = d.fallback
	return r, nil
}

func (d *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	dlog.Debug(d.callCtx, "Received gRPC Quit")
	d.cancel()
	return &empty.Empty{}, nil
}

func (d *service) Update(_ context.Context, table *rpc.Table) (*empty.Empty, error) {
	err := d.outbound.update(d.callCtx, table)
	return &empty.Empty{}, err
}

func (d *service) SetDnsSearchPath(_ context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	d.outbound.setSearchPath(d.callCtx, paths.Paths)
	return &empty.Empty{}, nil
}

func (d *service) SetManagerInfo(_ context.Context, info *rpc.ManagerInfo) (*empty.Empty, error) {
	d.outbound.setManagerInfo(d.callCtx, info)
	return &empty.Empty{}, nil
}

// run is the main function when executing as the daemon
func run(c context.Context, loggingDir, dns, fallback string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("telepresence %s must run as root", processName)
	}

	// Spoof the AppUserLogDir so that it returns the original user's logging dir rather than
	// the logging dir for the root user.
	c = filelocation.WithAppUserLogDir(c, loggingDir)

	c, err := logging.InitContext(c, processName)
	if err != nil {
		return err
	}

	d := &service{
		dns:      dns,
		fallback: fallback,
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
	}

	d.outbound, err = newOutbound(c, dns, fallback, false)
	if err != nil {
		return err
	}

	c = dgroup.WithGoroutineName(c, "/daemon")

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// The d.cancel will start a "quit" go-routine that will cause the group to initiate a a shutdown when it returns.
	d.cancel = func() { g.Go(processName+"-quit", d.quitConnector) }

	dlog.Info(c, "---")
	dlog.Infof(c, "Telepresence %s %s starting...", processName, client.DisplayVersion())
	dlog.Infof(c, "PID is %d", os.Getpid())
	dlog.Info(c, "")

	// server-dns runs a local DNS server that resolves *.cluster.local names.  Exactly where it
	// listens varies by platform.
	g.Go("server-dns", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-d.outbound.managerConfigured:
			return d.outbound.dnsServerWorker(ctx)
		}
	})

	// The 'Update' gRPC (below) call puts firewall updates in to a work queue;
	// tunnel-firewall-configurator is the worker process that reads that work queue.
	g.Go("firewall-configurator", func(ctx context.Context) error {
		return d.outbound.routerConfigurationWorker(ctx)
	})

	// server-grpc listens on /var/run/telepresence-daemon.socket and services gRPC requests
	// from the connector and from the CLI.
	g.Go("server-grpc", func(c context.Context) (err error) {
		var listener net.Listener
		defer func() {
			// Tell the firewall-configurator that we won't be sending it any more
			// updates.
			d.outbound.noMoreUpdates()

			// Error recovery.
			if perr := derror.PanicToError(recover()); perr != nil {
				dlog.Error(c, perr)
				if listener != nil {
					_ = listener.Close()
				}
				_ = os.Remove(client.DaemonSocketName)
			}
			if err != nil {
				dlog.Errorf(c, "Server ended with: %v", err)
			} else {
				dlog.Debug(c, "Server ended")
			}
		}()

		// Listen on unix domain socket
		dlog.Debug(c, "Server starting")
		d.callCtx = c
		listener, err = net.Listen("unix", client.DaemonSocketName)
		if err != nil {
			return errors.Wrap(err, "listen")
		}
		err = os.Chmod(client.DaemonSocketName, 0777)
		if err != nil {
			return errors.Wrap(err, "chmod")
		}

		svc := grpc.NewServer()
		rpc.RegisterDaemonServer(svc, d)
		go func() {
			<-c.Done()
			dlog.Debug(c, "Server stopping")
			svc.GracefulStop()
		}()
		return svc.Serve(listener)
	})

	err = g.Wait()
	if err != nil {
		dlog.Error(c, err)
	}
	return err
}

// quitConnector ensures that the connector quits gracefully.
func (d *service) quitConnector(c context.Context) error {
	if !client.SocketExists(client.ConnectorSocketName) {
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
