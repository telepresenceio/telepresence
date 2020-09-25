package daemon

import (
	"context"
	"net"
	"os"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/datawire/ambassador/pkg/supervisor"
)

var Help = `The Edge Control Daemon is a long-lived background component that manages
connections and network state.

Launch the Edge Control Daemon:
    sudo edgectl daemon

Examine the Daemon's log output in
    ` + edgectl.Logfile + `
to troubleshoot problems.
`

// daemon represents the state of the Edge Control Daemon
type daemon struct {
	network    Resource
	cluster    *KCluster
	bridge     Resource
	trafficMgr *TrafficManager
	intercepts []*Intercept
	dns        string
	fallback   string
}

// RunAsDaemon is the main function when executing as the daemon
func RunAsDaemon(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon must run as root")
	}

	d := &daemon{dns: dns, fallback: fallback}

	sup := supervisor.WithContext(context.Background())
	sup.Logger = SetUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: d.runGRPCService,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "signal",
		Requires: []string{"daemon"},
		Work:     WaitForSignal,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "setup",
		Requires: []string{"daemon"},
		Work: func(p *supervisor.Process) error {
			if err := d.MakeNetOverride(p); err != nil {
				return err
			}
			p.Ready()
			return nil
		},
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Edge Control daemon %s starting...", edgectl.DisplayVersion())
	sup.Logger.Printf("PID is %d", os.Getpid())
	runErrors := sup.Run()

	sup.Logger.Printf("")
	if len(runErrors) > 0 {
		sup.Logger.Printf("daemon has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Edge Control daemon %s is done.", edgectl.DisplayVersion())
	return errors.New("edgectl daemon has exited")
}

type grcpService struct {
	s *grpc.Server
	d *daemon
	p *supervisor.Process
}

func (s *grcpService) Version(_ context.Context, _ *rpc.Empty) (*rpc.VersionResponse, error) {
	return &rpc.VersionResponse{
		APIVersion: edgectl.ApiVersion,
		Version:    edgectl.Version,
	}, nil
}

func (s *grcpService) Status(_ context.Context, _ *rpc.Empty) (*rpc.StatusResponse, error) {
	return s.d.status(s.p), nil
}

func (s *grcpService) Connect(_ context.Context, cr *rpc.ConnectRequest) (*rpc.ConnectResponse, error) {
	return s.d.connect(s.p, cr), nil
}

func (s *grcpService) Disconnect(_ context.Context, _ *rpc.Empty) (*rpc.DisconnectResponse, error) {
	return s.d.disconnect(s.p), nil
}

func (s *grcpService) AddIntercept(_ context.Context, ir *rpc.InterceptRequest) (*rpc.InterceptResponse, error) {
	return s.d.addIntercept(s.p, ir), nil
}

func (s *grcpService) RemoveIntercept(_ context.Context, rr *rpc.RemoveInterceptRequest) (*rpc.InterceptResponse, error) {
	return s.d.removeIntercept(s.p, rr.Name), nil
}

func (s *grcpService) AvailableIntercepts(_ context.Context, _ *rpc.Empty) (*rpc.AvailableInterceptsResponse, error) {
	return s.d.availableIntercepts(s.p), nil
}

func (s *grcpService) ListIntercepts(_ context.Context, _ *rpc.Empty) (*rpc.ListInterceptsResponse, error) {
	return s.d.listIntercepts(s.p), nil
}

func (s *grcpService) Pause(ctx context.Context, empty *rpc.Empty) (*rpc.PauseResponse, error) {
	return s.d.pause(s.p), nil
}

func (s *grcpService) Resume(ctx context.Context, empty *rpc.Empty) (*rpc.ResumeResponse, error) {
	return s.d.resume(s.p), nil
}

func (s *grcpService) Quit(ctx context.Context, empty *rpc.Empty) (*rpc.Empty, error) {
	// GracefulStop() must be called in a separate go routine since it will await the
	// client disconnect. That doesn't happen until this function returns.
	go s.s.GracefulStop()
	s.p.Supervisor().Shutdown()
	return &rpc.Empty{}, nil
}

func (d *daemon) runGRPCService(daemonProc *supervisor.Process) error {
	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", edgectl.DaemonSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	err = os.Chmod(edgectl.DaemonSocketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}

	grpcServer := grpc.NewServer()
	rpc.RegisterDaemonServer(grpcServer, &grcpService{
		s: grpcServer,
		d: d,
		p: daemonProc,
	})

	daemonProc.Ready()
	Notify(daemonProc, "Running")
	defer Notify(daemonProc, "Shutting down...")
	return daemonProc.DoClean(func() error {
		return errors.Wrap(grpcServer.Serve(unixListener), "gRCP server")
	}, func() error {
		grpcServer.Stop()
		return nil
	})
}

func (d *daemon) pause(p *supervisor.Process) *rpc.PauseResponse {
	r := rpc.PauseResponse{}
	switch {
	case d.network == nil:
		r.Error = rpc.PauseResponse_AlreadyPaused
	case d.cluster != nil:
		r.Error = rpc.PauseResponse_ConnectedToCluster
	default:
		if err := d.network.Close(); err != nil {
			r.Error = rpc.PauseResponse_UnexpectedPauseError
			r.ErrorText = err.Error()
			p.Logf("pause: %v", err)
		}
		d.network = nil
	}
	return &r
}

func (d *daemon) resume(p *supervisor.Process) *rpc.ResumeResponse {
	r := rpc.ResumeResponse{}
	if d.network != nil {
		if d.network.IsOkay() {
			r.Error = rpc.ResumeResponse_NotPaused
		} else {
			r.Error = rpc.ResumeResponse_ReEstablishing
		}
	} else if err := d.MakeNetOverride(p); err != nil {
		r.Error = rpc.ResumeResponse_UnexpectedResumeError
		r.ErrorText = err.Error()
		p.Logf("resume: %v", err)
	}
	return &r
}
