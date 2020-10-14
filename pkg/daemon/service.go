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
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

var Help = `The Telepresence Daemon is a long-lived background component that manages
connections and network state.

Launch the Telepresence Daemon:
    sudo telepresence service

Examine the Daemon's log output in
    ` + common.Logfile + `
to troubleshoot problems.
`

// daemon represents the state of the Telepresence Daemon
type service struct {
	rpc.UnimplementedDaemonServer
	network  common.Resource
	dns      string
	fallback string
	grpc     *grpc.Server
	hClient  *http.Client
	p        *supervisor.Process
}

// Run is the main function when executing as the daemon
func Run(dns, fallback string) error {
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

	sup := supervisor.WithContext(context.Background())
	sup.Logger = setUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: d.runGRPCService,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "setup",
		Requires: []string{"daemon"},
		Work: func(p *supervisor.Process) error {
			if err := d.makeNetOverride(p); err != nil {
				return err
			}
			p.Ready()
			return nil
		},
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Telepresence daemon %s starting...", common.DisplayVersion())
	sup.Logger.Printf("PID is %d", os.Getpid())
	runErrors := sup.Run()

	sup.Logger.Printf("")
	if len(runErrors) > 0 {
		sup.Logger.Printf("daemon has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Telepresence daemon %s is done.", common.DisplayVersion())
	return errors.New("telepresence daemon has exited")
}

func (d *service) Logger(server rpc.Daemon_LoggerServer) error {
	lg := d.p.Supervisor().Logger
	for {
		msg, err := server.Recv()
		if err == io.EOF {
			return server.SendAndClose(&rpc.Empty{})
		}
		if err != nil {
			return err
		}
		lg.Printf(msg.Text)
	}
}

func (d *service) Version(_ context.Context, _ *rpc.Empty) (*rpc.VersionResponse, error) {
	return &rpc.VersionResponse{
		APIVersion: common.ApiVersion,
		Version:    common.Version,
	}, nil
}

func (d *service) Status(_ context.Context, _ *rpc.Empty) (*rpc.DaemonStatusResponse, error) {
	r := &rpc.DaemonStatusResponse{}
	if d.network == nil {
		r.Error = rpc.DaemonStatusResponse_Paused
		return r, nil
	}
	if !d.network.IsOkay() {
		r.Error = rpc.DaemonStatusResponse_NoNetwork
		return r, nil
	}
	return r, nil
}

func (d *service) Pause(_ context.Context, _ *rpc.Empty) (*rpc.PauseResponse, error) {
	r := rpc.PauseResponse{}
	switch {
	case d.network == nil:
		r.Error = rpc.PauseResponse_AlreadyPaused
	case common.SocketExists(common.ConnectorSocketName):
		r.Error = rpc.PauseResponse_ConnectedToCluster
	default:
		if err := d.network.Close(); err != nil {
			r.Error = rpc.PauseResponse_UnexpectedPauseError
			r.ErrorText = err.Error()
			d.p.Logf("pause: %v", err)
		}
		d.network = nil
	}
	return &r, nil
}

func (d *service) Resume(_ context.Context, _ *rpc.Empty) (*rpc.ResumeResponse, error) {
	r := rpc.ResumeResponse{}
	if d.network != nil {
		if d.network.IsOkay() {
			r.Error = rpc.ResumeResponse_NotPaused
		} else {
			r.Error = rpc.ResumeResponse_ReEstablishing
		}
	} else if err := d.makeNetOverride(d.p); err != nil {
		r.Error = rpc.ResumeResponse_UnexpectedResumeError
		r.ErrorText = err.Error()
		d.p.Logf("resume: %v", err)
	}
	return &r, nil
}

func (d *service) Quit(_ context.Context, _ *rpc.Empty) (*rpc.Empty, error) {
	d.p.Supervisor().Shutdown()
	return &rpc.Empty{}, nil
}

func (d *service) runGRPCService(p *supervisor.Process) error {
	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", common.DaemonSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	err = os.Chmod(common.DaemonSocketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}

	grpcServer := grpc.NewServer()
	d.grpc = grpcServer
	d.p = p
	rpc.RegisterDaemonServer(grpcServer, d)

	go d.handleSignalsAndShutdown()

	p.Ready()
	return errors.Wrap(grpcServer.Serve(unixListener), "daemon gRCP server")
}

// handleSignalsAndShutdown ensures that the daemon quits gracefully when receiving a signal
// or when the supervisor wants to shutdown.
func (d *service) handleSignalsAndShutdown() {
	defer d.grpc.GracefulStop()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-interrupt:
		d.p.Logf("Received signal %s", sig)
	case <-d.p.Shutdown():
		d.p.Log("Shutting down")
	}

	if !common.SocketExists(common.ConnectorSocketName) {
		return
	}
	conn, err := grpc.Dial(common.SocketURL(common.ConnectorSocketName), grpc.WithInsecure())
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = rpc.NewConnectorClient(conn).Quit(d.p.Context(), &rpc.Empty{})
}
