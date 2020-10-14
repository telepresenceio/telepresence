package connector

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

var Help = `The Edge Control Connect is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Edge Control Connector:
    edgectl connect

The Connector uses the Daemon's log so its output can be found in
    ` + common.Logfile + `
to troubleshoot problems.
`

// service represents the state of the Edge Control Connector
type service struct {
	rpc.UnimplementedConnectorServer
	daemon     rpc.DaemonClient
	cluster    *k8sCluster
	bridge     common.Resource
	trafficMgr *trafficManager
	intercepts []*intercept
	grpc       *grpc.Server
	p          *supervisor.Process
}

// Run is the main function when executing as the connector
func Run() error {
	// establish a connection to the daemon gRPC service
	conn, err := grpc.Dial(common.SocketURL(common.DaemonSocketName), grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()

	d := &service{daemon: rpc.NewDaemonClient(conn)}
	sup := supervisor.WithContext(context.Background())
	if err = d.setUpLogging(sup); err != nil {
		return err
	}

	sup.Supervise(&supervisor.Worker{
		Name: "connector",
		Work: d.runGRPCService,
	})
	runErrors := sup.Run()

	if len(runErrors) > 0 {
		sup.Logger.Printf("collector has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Edge Control collector %s is done.", common.DisplayVersion())
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
func (s *service) runGRPCService(p *supervisor.Process) error {
	p.Log("---")
	p.Logf("Edge Control Collector %s starting...", common.DisplayVersion())
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

	go s.handleSignalsAndShutdown()

	p.Ready()
	return errors.Wrap(s.grpc.Serve(unixListener), "connector gRCP server")
}

// connect the daemon to a cluster
func (s *service) connect(p *supervisor.Process, cr *rpc.ConnectRequest) *rpc.ConnectResponse {
	reporter := &metriton.Reporter{
		Application:  "edgectl",
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

	bridge, err := common.CheckedRetryingCommand(
		p,
		"bridge",
		common.GetExe(),
		[]string{"teleproxy", "bridge", cluster.ctx, cluster.namespace},
		checkBridge,
		15*time.Second,
	)
	if err != nil {
		r.Error = rpc.ConnectResponse_BridgeFailed
		r.ErrorText = err.Error()
		// No point in continuing without a bridge
		s.p.Supervisor().Shutdown()
		return r
	}
	s.bridge = bridge
	s.cluster.setBridgeCheck(s.bridge.IsOkay)
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

func (d *daemonLogger) Printf(format string, v ...interface{}) {
	txt := fmt.Sprintf(format, v...)
	err := d.stream.Send(&rpc.LogMessage{Text: txt})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error while sending log message to daemon: %s\nOriginal message was %q\n", err.Error(), txt)
	}
}

// checkBridge checks the status of teleproxy bridge by doing the equivalent of
//  curl http://traffic-proxy.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func checkBridge(_ *supervisor.Process) error {
	address := "traffic-proxy.svc:8022"
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		return errors.Wrap(err, "tcp connect")
	}
	if conn != nil {
		defer conn.Close()
		msg, _, err := bufio.NewReader(conn).ReadLine()
		if err != nil {
			return errors.Wrap(err, "tcp read")
		}
		if !strings.Contains(string(msg), "SSH") {
			return fmt.Errorf("expected SSH prompt, got: %v", string(msg))
		}
	} else {
		return fmt.Errorf("fail to establish tcp connection to %v", address)
	}
	return nil
}

// handleSignalsAndShutdown ensures that the connector quits gracefully when receiving a signal
// or when the supervisor wants to shutdown.
func (s *service) handleSignalsAndShutdown() {
	defer s.grpc.GracefulStop()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-interrupt:
		s.p.Logf("Received signal %s", sig)
	case <-s.p.Shutdown():
		s.p.Log("Shutting down")
	}

	cluster := s.cluster
	if cluster == nil {
		return
	}
	s.cluster = nil

	bridge := s.bridge
	trafficMgr := s.trafficMgr

	s.bridge = nil
	s.trafficMgr = nil

	defer cluster.Close()

	_ = s.clearIntercepts(s.p)
	if bridge != nil {
		cluster.setBridgeCheck(nil) // Stop depending on this bridge
		_ = bridge.Close()
	}
	if trafficMgr != nil {
		_ = trafficMgr.Close()
	}
}
