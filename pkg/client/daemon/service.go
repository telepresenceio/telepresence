package daemon

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/natefinch/lumberjack.v2"

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

var help = `The Telepresence Daemon is a long-lived background component that manages
connections and network state.

Launch the Telepresence Daemon:
    sudo telepresence service

Examine the Daemon's log output in
    ` + client.Logfile + `
to troubleshoot problems.
`

// daemon represents the state of the Telepresence Daemon
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
		Use:    "daemon-foreground",
		Short:  "Launch Telepresence Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		Long:   help,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(args[0], args[1])
		},
	}
}

// setUpLogging sets up standard Telepresence Daemon logging
func setUpLogging(c context.Context) context.Context {
	loggingToTerminal := terminal.IsTerminal(int(os.Stdout.Fd()))
	logger := logrus.StandardLogger()
	if loggingToTerminal {
		logger.Formatter = logging.NewFormatter("15:04:05")
	} else {
		logger.Formatter = logging.NewFormatter("2006/01/02 15:04:05")
		log.SetOutput(logger.Writer())
		logger.SetOutput(&lumberjack.Logger{
			Filename:   client.Logfile,
			MaxSize:    10,   // megabytes
			MaxBackups: 3,    // in the same directory
			MaxAge:     60,   // days
			LocalTime:  true, // rotated logfiles use local time names
		})
	}
	logger.Level = logrus.DebugLevel
	return dlog.WithLogger(c, dlog.WrapLogrus(logger))
}

func (d *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (d *service) callContext(_ context.Context) context.Context {
	return d.callCtx
}

func (d *service) Status(_ context.Context, _ *empty.Empty) (*rpc.DaemonStatus, error) {
	r := &rpc.DaemonStatus{}
	if d.outbound == nil {
		r.Error = rpc.DaemonStatus_PAUSED
		return r, nil
	}
	r.Dns = d.dns
	r.Fallback = d.fallback
	return r, nil
}

func (d *service) Pause(_ context.Context, _ *empty.Empty) (*rpc.PauseInfo, error) {
	r := rpc.PauseInfo{}
	switch {
	case d.outbound == nil:
		r.Error = rpc.PauseInfo_ALREADY_PAUSED
	case client.SocketExists(client.ConnectorSocketName):
		r.Error = rpc.PauseInfo_CONNECTED_TO_CLUSTER
	default:
		d.outbound.shutdown()
		d.outbound = nil
	}
	return &r, nil
}

func (d *service) Resume(c context.Context, _ *empty.Empty) (*rpc.ResumeInfo, error) {
	r := rpc.ResumeInfo{}
	if d.outbound != nil {
		r.Error = rpc.ResumeInfo_NOT_PAUSED
	} else {
		c := d.callContext(c)
		outbound, err := start(c, d.dns, d.fallback, false)
		if err != nil {
			r.Error = rpc.ResumeInfo_UNEXPECTED_RESUME_ERROR
			r.ErrorText = err.Error()
			dlog.Infof(c, "resume: %v", err)
		}
		d.outbound = outbound
	}
	return &r, nil
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
func run(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("telepresence daemon must run as root")
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

	c := setUpLogging(context.Background())
	g := dgroup.NewGroup(c, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	// The d.cancel will start a "quit" go-routine that will cause the group to initiate a a shutdown when it returns.
	d.cancel = func() { g.Go("daemon-quit", d.quitConnector) }
	g.Go("daemon", func(c context.Context) error {
		dlog.Info(c, "---")
		dlog.Infof(c, "Telepresence daemon %s starting...", client.DisplayVersion())
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

	err := g.Wait()
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
