package daemon

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
	"google.golang.org/grpc"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	rpc "github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/iptables"
	"github.com/datawire/telepresence2/pkg/rpc/version"
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
	rpc.UnimplementedDaemonServer
	networkShutdown func()
	dns             string
	fallback        string
	grpc            *grpc.Server
	hClient         *http.Client
	ipTables        *ipTables
	callCtx         context.Context
	cancel          context.CancelFunc
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
		logger.Formatter = client.NewFormatter("15:04:05")
	} else {
		logger.Formatter = client.NewFormatter("2006/01/02 15:04:05")
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

func (d *service) Logger(server rpc.Daemon_LoggerServer) error {
	for {
		msg, err := server.Recv()
		if err == io.EOF {
			return server.SendAndClose(&empty.Empty{})
		}
		if err != nil {
			return err
		}
		_, _ = logrus.StandardLogger().Out.Write(msg.Text)
	}
}

func (d *service) Version(_ context.Context, _ *empty.Empty) (*version.VersionInfo, error) {
	return &version.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) callContext(_ context.Context) context.Context {
	return s.callCtx
}

func (d *service) Status(_ context.Context, _ *empty.Empty) (*rpc.DaemonStatus, error) {
	r := &rpc.DaemonStatus{}
	if d.networkShutdown == nil {
		r.Error = rpc.DaemonStatus_PAUSED
		return r, nil
	}
	return r, nil
}

func (d *service) Pause(_ context.Context, _ *empty.Empty) (*rpc.PauseInfo, error) {
	r := rpc.PauseInfo{}
	switch {
	case d.networkShutdown == nil:
		r.Error = rpc.PauseInfo_ALREADY_PAUSED
	case client.SocketExists(client.ConnectorSocketName):
		r.Error = rpc.PauseInfo_CONNECTED_TO_CLUSTER
	default:
		d.networkShutdown()
		d.networkShutdown = nil
	}
	return &r, nil
}

func (d *service) Resume(c context.Context, _ *empty.Empty) (*rpc.ResumeInfo, error) {
	r := rpc.ResumeInfo{}
	if d.networkShutdown != nil {
		r.Error = rpc.ResumeInfo_NOT_PAUSED
	} else {
		c := d.callContext(c)
		ipTables, shutdown, err := start(c, d.dns, d.fallback, false)
		if err != nil {
			r.Error = rpc.ResumeInfo_UNEXPECTED_RESUME_ERROR
			r.ErrorText = err.Error()
			dlog.Infof(c, "resume: %v", err)
		}
		d.ipTables = ipTables
		d.networkShutdown = shutdown
	}
	return &r, nil
}

func (d *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	d.cancel()
	return &empty.Empty{}, nil
}

func (d *service) AllIPTables(_ context.Context, _ *empty.Empty) (*rpc.Tables, error) {
	return d.ipTables.getAll(), nil
}

func (d *service) DeleteIPTable(_ context.Context, name *rpc.TableName) (*empty.Empty, error) {
	d.ipTables.delete(name.Name)
	return &empty.Empty{}, nil
}

func (d *service) IPTable(_ context.Context, name *rpc.TableName) (*iptables.Table, error) {
	return d.ipTables.get(name.Name), nil
}

func (d *service) Update(_ context.Context, table *iptables.Table) (*empty.Empty, error) {
	d.ipTables.update(table)
	dns.Flush()
	return &empty.Empty{}, nil
}

func (d *service) DnsSearchPath(_ context.Context, _ *empty.Empty) (*rpc.Paths, error) {
	return &rpc.Paths{Paths: d.ipTables.searchPath()}, nil
}

func (d *service) SetDnsSearchPath(_ context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	d.ipTables.setSearchPath(paths.Paths)
	return &empty.Empty{}, nil
}

// run is the main function when executing as the daemon
func run(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("telepresence daemon must run as root")
	}

	var listener net.Listener
	defer func() {
		if listener != nil {
			_ = listener.Close()
		}
		_ = os.Remove(client.DaemonSocketName)
	}()

	// Listen on unix domain socket
	listener, err := net.Listen("unix", client.DaemonSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	err = os.Chmod(client.DaemonSocketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
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
		}},
		grpc: grpc.NewServer()}

	rpc.RegisterDaemonServer(d.grpc, d)

	g := dgroup.NewGroup(context.Background(), dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true})

	g.Go("daemon", func(c context.Context) error {
		c = setUpLogging(c)
		hc := c

		dlog.Info(c, "---")
		dlog.Infof(c, "Telepresence daemon %s starting...", client.DisplayVersion())
		dlog.Infof(c, "PID is %d", os.Getpid())
		dlog.Info(c, "")

		c, d.cancel = context.WithCancel(c)
		d.callCtx = c
		sg := dgroup.NewGroup(c, dgroup.GroupConfig{})
		sg.Go("outbound", func(c context.Context) error {
			d.ipTables, d.networkShutdown, err = start(c, dns, fallback, false)
			return err
		})
		sg.Go("teardown", func(c context.Context) error {
			return d.handleShutdown(c, hc)
		})
		err := d.grpc.Serve(listener)
		listener = nil
		if err != nil {
			dlog.Error(c, err.Error())
		} else {
			dlog.Infof(c, "Telepresence daemon %s is done.", client.DisplayVersion())
		}
		return err
	})
	return g.Wait()
}

// handleShutdown ensures that the daemon quits gracefully when the context is cancelled.
func (d *service) handleShutdown(c, hc context.Context) error {
	defer d.grpc.GracefulStop()

	<-c.Done()
	c = hc
	dlog.Info(c, "Shutting down")

	if !client.SocketExists(client.ConnectorSocketName) {
		return nil
	}
	conn, err := client.DialSocket(client.ConnectorSocketName)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = connector.NewConnectorClient(conn).Quit(c, &empty.Empty{})
	return err
}
