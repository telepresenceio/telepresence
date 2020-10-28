package daemon

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	rpc "github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/iptables"
	"github.com/datawire/telepresence2/pkg/rpc/version"
)

var Help = `The Telepresence Daemon is a long-lived background component that manages
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
	p               *supervisor.Process
}

func Command() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon-foreground",
		Short:  "Launch Telepresence Daemon in the foreground (debug)",
		Args:   cobra.ExactArgs(2),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(args[0], args[1])
		},
	}
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

	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)
	sup.Logger = setUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: func(p *supervisor.Process) error { return d.runGRPCService(p, cancel) },
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "setup",
		Requires: []string{"daemon"},
		Work: func(p *supervisor.Process) error {
			ipTables, shutdown, err := start(p, dns, fallback, false)
			if err != nil {
				return err
			}
			d.ipTables = ipTables
			d.networkShutdown = shutdown
			p.Ready()
			return nil
		},
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Telepresence daemon %s starting...", client.DisplayVersion())
	sup.Logger.Printf("PID is %d", os.Getpid())
	runErrors := sup.Run()

	sup.Logger.Printf("")
	if len(runErrors) > 0 {
		sup.Logger.Printf("daemon has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Telepresence daemon %s is done.", client.DisplayVersion())
	return errors.New("telepresence daemon has exited")
}

func (d *service) Logger(server rpc.Daemon_LoggerServer) error {
	lg := d.p.Supervisor().Logger
	for {
		msg, err := server.Recv()
		if err == io.EOF {
			return server.SendAndClose(&empty.Empty{})
		}
		if err != nil {
			return err
		}
		lg.Printf(msg.Text)
	}
}

func (d *service) Version(_ context.Context, _ *empty.Empty) (*version.VersionInfo, error) {
	return &version.VersionInfo{
		ApiVersion: client.ApiVersion,
		Version:    client.Version,
	}, nil
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

func (d *service) Resume(_ context.Context, _ *empty.Empty) (*rpc.ResumeInfo, error) {
	r := rpc.ResumeInfo{}
	if d.networkShutdown != nil {
		r.Error = rpc.ResumeInfo_NOT_PAUSED
	} else {
		ipTables, shutdown, err := start(d.p, d.dns, d.fallback, false)
		if err != nil {
			r.Error = rpc.ResumeInfo_UNEXPECTED_RESUME_ERROR
			r.ErrorText = err.Error()
			d.p.Logf("resume: %v", err)
		}
		d.ipTables = ipTables
		d.networkShutdown = shutdown
	}
	return &r, nil
}

func (d *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	d.p.Supervisor().Shutdown()
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
	return &empty.Empty{}, nil
}

func (d *service) DnsSearchPath(_ context.Context, _ *empty.Empty) (*rpc.Paths, error) {
	return &rpc.Paths{Paths: d.ipTables.searchPath()}, nil
}

func (d *service) SetDnsSearchPath(_ context.Context, paths *rpc.Paths) (*empty.Empty, error) {
	d.ipTables.setSearchPath(paths.Paths)
	return &empty.Empty{}, nil
}

func (d *service) runGRPCService(p *supervisor.Process, cancel context.CancelFunc) error {
	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", client.DaemonSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	err = os.Chmod(client.DaemonSocketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}

	grpcServer := grpc.NewServer()
	d.grpc = grpcServer
	d.p = p
	rpc.RegisterDaemonServer(grpcServer, d)

	go d.handleSignalsAndShutdown(cancel)

	p.Ready()
	return errors.Wrap(grpcServer.Serve(unixListener), "daemon gRCP server")
}

// handleSignalsAndShutdown ensures that the daemon quits gracefully when receiving a signal
// or when the supervisor wants to shutdown.
func (d *service) handleSignalsAndShutdown(cancel context.CancelFunc) {
	defer d.grpc.GracefulStop()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-interrupt:
		d.p.Logf("Received signal %s", sig)
		cancel()
	case <-d.p.Shutdown():
		d.p.Log("Shutting down")
	}

	if !client.SocketExists(client.ConnectorSocketName) {
		return
	}
	conn, err := grpc.Dial(client.SocketURL(client.ConnectorSocketName), grpc.WithInsecure())
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = connector.NewConnectorClient(conn).Quit(d.p.Context(), &empty.Empty{})
}
