package connector

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/bridges"
	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

var Help = `The Telepresence Connect is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence Connector:
    telepresence connect

The Connector uses the Daemon's log so its output can be found in
    ` + common.Logfile + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Connector
type service struct {
	rpc.UnimplementedConnectorServer
	daemon     rpc.DaemonClient
	cluster    *k8sCluster
	bridge     bridges.Service
	trafficMgr *trafficManager
	intercepts []*intercept
	grpc       *grpc.Server
	p          *supervisor.Process
}

func Command() *cobra.Command {
	return &cobra.Command{
		Use:    "connector-foreground",
		Short:  "Launch Telepresence Connector in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return run()
		},
	}
}

// run is the main function when executing as the connector
func run() error {
	// establish a connection to the daemon gRPC service
	conn, err := grpc.Dial(common.SocketURL(common.DaemonSocketName), grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()

	d := &service{daemon: rpc.NewDaemonClient(conn)}
	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)
	if err = d.setUpLogging(sup); err != nil {
		cancel()
		return err
	}

	sup.Supervise(&supervisor.Worker{
		Name: "connector",
		Work: func(p *supervisor.Process) error {
			return d.runGRPCService(p, cancel)
		},
	})
	runErrors := sup.Run()

	if len(runErrors) > 0 {
		sup.Logger.Printf("collector has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Telepresence connector %s is done.", common.DisplayVersion())
	return nil
}

func (s *service) Version(_ context.Context, _ *rpc.Empty) (*rpc.VersionResponse, error) {
	return &rpc.VersionResponse{
		APIVersion: common.ApiVersion,
		Version:    common.Version,
	}, nil
}

func (s *service) Status(_ context.Context, _ *rpc.Empty) (*rpc.ConnectorStatusResponse, error) {
	return s.status(s.p), nil
}

func (s *service) Connect(_ context.Context, cr *rpc.ConnectRequest) (*rpc.ConnectResponse, error) {
	return s.connect(s.p, cr), nil
}

func (s *service) AddIntercept(_ context.Context, ir *rpc.InterceptRequest) (*rpc.InterceptResponse, error) {
	return s.addIntercept(s.p, ir), nil
}

func (s *service) RemoveIntercept(_ context.Context, rr *rpc.RemoveInterceptRequest) (*rpc.InterceptResponse, error) {
	return s.removeIntercept(s.p, rr.Name), nil
}

func (s *service) AvailableIntercepts(_ context.Context, _ *rpc.Empty) (*rpc.AvailableInterceptsResponse, error) {
	return s.availableIntercepts(s.p), nil
}

func (s *service) ListIntercepts(_ context.Context, _ *rpc.Empty) (*rpc.ListInterceptsResponse, error) {
	return s.listIntercepts(s.p), nil
}

func (s *service) Quit(_ context.Context, _ *rpc.Empty) (*rpc.Empty, error) {
	s.p.Supervisor().Shutdown()
	return &rpc.Empty{}, nil
}

// setUpLogging connects to the daemon logger and assigns a wrapper for it to the
// supervisors logger.
func (s *service) setUpLogging(sup *supervisor.Supervisor) error {
	logStream, err := s.daemon.Logger(context.Background())
	if err == nil {
		sup.Logger = &daemonLogger{stream: logStream}
	}
	return err
}

// runGRPCService is the main gRPC server loop.
func (s *service) runGRPCService(p *supervisor.Process, cancel func()) error {
	p.Log("---")
	p.Logf("Telepresence Connector %s starting...", common.DisplayVersion())
	p.Logf("PID is %d", os.Getpid())
	p.Log("")

	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", common.ConnectorSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}
	s.grpc = grpc.NewServer()
	s.p = p
	rpc.RegisterConnectorServer(s.grpc, s)

	go s.handleSignalsAndShutdown(cancel)

	p.Ready()
	return errors.Wrap(s.grpc.Serve(unixListener), "connector gRCP server")
}

// connect the daemon to a cluster
func (s *service) connect(p *supervisor.Process, cr *rpc.ConnectRequest) *rpc.ConnectResponse {
	reporter := &metriton.Reporter{
		Application:  "telepresence",
		Version:      common.Version,
		GetInstallID: func(_ *metriton.Reporter) (string, error) { return cr.InstallID, nil },
		BaseMetadata: map[string]interface{}{"mode": "daemon"},
	}

	if _, err := reporter.Report(p.Context(), map[string]interface{}{"action": "connect"}); err != nil {
		p.Logf("report failed: %+v", err)
	}

	// Sanity checks
	r := &rpc.ConnectResponse{}
	if s.cluster != nil {
		r.Error = rpc.ConnectResponse_AlreadyConnected
		return r
	}
	if s.bridge != nil {
		r.Error = rpc.ConnectResponse_Disconnecting
		return r
	}

	p.Logf("Connecting to traffic manager in namespace %s...", cr.ManagerNS)
	cluster, err := trackKCluster(p, cr.Context, cr.Namespace, cr.Args)
	if err != nil {
		r.Error = rpc.ConnectResponse_ClusterFailed
		r.ErrorText = err.Error()
		return r
	}
	s.cluster = cluster

	previewHost, err := cluster.getClusterPreviewHostname(p)
	if err != nil {
		p.Logf("get preview URL hostname: %+v", err)
		previewHost = ""
	}

	br := bridges.NewService("", cluster.ctx, cluster.namespace)
	if err = br.Start(p); err != nil {
		r.Error = rpc.ConnectResponse_BridgeFailed
		r.ErrorText = err.Error()
		// No point in continuing without a bridge
		s.p.Supervisor().Shutdown()
		return r
	}
	s.bridge = br
	s.cluster.setBridgeCheck(func() bool {
		return br.Check(p)
	})

	p.Logf("Connected to context %s (%s)", s.cluster.context(), s.cluster.server())

	r.ClusterContext = s.cluster.context()
	r.ClusterServer = s.cluster.server()

	tmgr, err := newTrafficManager(p, s.cluster, cr.ManagerNS, cr.InstallID, cr.IsCI)
	if err != nil {
		p.Logf("Unable to connect to TrafficManager: %s", err)
		r.Error = rpc.ConnectResponse_TrafficManagerFailed
		r.ErrorText = err.Error()
		if cr.InterceptEnabled {
			// No point in continuing without a traffic manager
			s.p.Supervisor().Shutdown()
		}
		return r
	}
	tmgr.previewHost = previewHost
	s.trafficMgr = tmgr

	if !cr.InterceptEnabled {
		return r
	}

	// Wait for traffic manager to connect
	maxAttempts := 30 * 4 // 30 seconds max wait
	attempts := 0
	p.Log("Waiting for TrafficManager to connect")
	for ; !tmgr.IsOkay() && attempts < maxAttempts; attempts++ {
		if s.trafficMgr.apiErr != nil {
			r.Error = rpc.ConnectResponse_TrafficManagerFailed
			r.ErrorText = s.trafficMgr.apiErr.Error()
			// No point in continuing without a traffic manager
			s.p.Supervisor().Shutdown()
			break
		}
		time.Sleep(time.Second / 4)
	}
	if attempts == maxAttempts {
		r.Error = rpc.ConnectResponse_TrafficManagerFailed
		r.ErrorText = "Timeout waiting for traffic manager"
		p.Log(r.ErrorText)
		s.p.Supervisor().Shutdown()
	}
	return r
}

// daemonLogger is a supervisor.Logger implementation that sends log messages to the daemon
type daemonLogger struct {
	stream rpc.Daemon_LoggerClient
}

// Printf implements the supervisor.Logger interface
func (d *daemonLogger) Printf(format string, v ...interface{}) {
	txt := fmt.Sprintf(format, v...)
	err := d.stream.Send(&rpc.LogMessage{Text: txt})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error while sending log message to daemon: %s\nOriginal message was %q\n", err.Error(), txt)
	}
}

// handleSignalsAndShutdown ensures that the connector quits gracefully when receiving a signal
// or when the supervisor wants to shutdown.
func (s *service) handleSignalsAndShutdown(cancel func()) {
	defer s.grpc.GracefulStop()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		select {
		case sig := <-interrupt:
			s.p.Logf("Received signal %s", sig)
			if sig == syscall.SIGHUP {
				if bridge := s.bridge; bridge != nil {
					bridge.Restart()
				}
				continue
			}
			cancel()
		case <-s.p.Shutdown():
			s.p.Log("Shutting down")
		}
		break
	}

	cluster := s.cluster
	if cluster == nil {
		return
	}
	s.cluster = nil
	trafficMgr := s.trafficMgr

	s.trafficMgr = nil

	defer cluster.Close()

	s.clearIntercepts(s.p)
	if trafficMgr != nil {
		_ = trafficMgr.Close()
	}
	s.bridge = nil
}
