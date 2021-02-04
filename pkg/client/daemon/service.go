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

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/rpc/v2/common"
	"github.com/datawire/telepresence2/rpc/v2/connector"
	rpc "github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/v2/pkg/client/logging"
)

const processName = "daemon"
const titleName = "Daemon"

var help = `The Telepresence ` + titleName + ` is a long-lived background component that manages
connections and network state.

Launch the Telepresence ` + titleName + `:
    sudo telepresence service

Examine the ` + titleName + `'s log output in
    ` + filepath.Join(logging.Dir(), processName+".log") + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Daemon
type service struct {
	rpc.UnsafeDaemonServer
	dns      string
	fallback string
	hClient  *http.Client
	outbound *outbound
	callCtx  context.Context
	cancel   context.CancelFunc
}

// Command returns the telepresence sub-command "daemon-foreground"
func Command() *cobra.Command {
	return &cobra.Command{
		Use:    processName + "-foreground",
		Short:  "Launch Telepresence " + titleName + " in the foreground (debug)",
		Args:   cobra.ExactArgs(3),
		Hidden: true,
		Long:   help,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(args[0], args[1], args[2])
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
	d.outbound.update(table)
	dns.Flush()
	return &empty.Empty{}, nil
}

func (d *service) SetDnsSearchPath(_ context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	d.outbound.setSearchPath(d.callCtx, paths.Paths)
	return &empty.Empty{}, nil
}

// run is the main function when executing as the daemon
func run(loggingDir, dns, fallback string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("telepresence %s must run as root", processName)
	}

	// Reassign logging.Dir function so that it returns the original users logging dir rather
	// than the logging.Dir for the root user.
	logging.Dir = func() string {
		return loggingDir
	}

	c, err := logging.InitContext(context.Background(), processName)
	if err != nil {
		return err
	}

	d := &service{dns: dns, fallback: fallback, hClient: &http.Client{
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
		}}}

	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// The d.cancel will start a "quit" go-routine that will cause the group to initiate a a shutdown when it returns.
	d.cancel = func() { g.Go(processName+"-quit", d.quitConnector) }
	g.Go(processName, func(c context.Context) error {
		dlog.Info(c, "---")
		dlog.Infof(c, "Telepresence %s %s starting...", processName, client.DisplayVersion())
		dlog.Infof(c, "PID is %d", os.Getpid())
		dlog.Info(c, "")

		g := dgroup.NewGroup(c, dgroup.GroupConfig{})
		g.Go("outbound", func(c context.Context) (err error) {
			d.outbound, err = start(c, dns, fallback, false)
			return err
		})

		g.Go("service", func(c context.Context) (err error) {
			var listener net.Listener
			defer func() {
				if perr := dutil.PanicToError(recover()); perr != nil {
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
		return g.Wait()
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
