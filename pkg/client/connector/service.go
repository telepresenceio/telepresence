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
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/client"
	manager "github.com/datawire/telepresence2/pkg/rpc"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/version"
)

var help = `The Telepresence Connect is a background component that manages a connection. It
requires that a daemon is already running.

Launch the Telepresence Connector:
    telepresence connect

The Connector uses the Daemon's log so its output can be found in
    ` + client.Logfile + `
to troubleshoot problems.
`

// service represents the state of the Telepresence Connector
type service struct {
	rpc.UnimplementedConnectorServer
	daemon     daemon.DaemonClient
	cluster    *k8sCluster
	bridge     *bridge
	trafficMgr *trafficManager
	grpc       *grpc.Server
	p          *supervisor.Process
}

// Command returns the CLI sub-command for "connector-foreground"
func Command() *cobra.Command {
	var init bool
	c := &cobra.Command{
		Use:    "connector-foreground",
		Short:  "Launch Telepresence Connector in the foreground (debug)",
		Args:   cobra.ExactArgs(0),
		Hidden: true,
		Long:   help,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(init)
		},
	}
	flags := c.Flags()
	flags.BoolVar(&init, "init", false, "initialize running connector (for debugging)")
	return c
}

// run is the main function when executing as the connector
func run(init bool) error {
	// establish a connection to the daemon gRPC service
	conn, err := client.DialSocket(client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer conn.Close()

	d := &service{daemon: daemon.NewDaemonClient(conn)}
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
	if init {
		sup.Supervise(&supervisor.Worker{
			Name:     "init",
			Requires: []string{"connector"},
			Work: func(p *supervisor.Process) error {
				_, err := d.Connect(p.Context(), &rpc.ConnectRequest{InstallId: "dummy-id"})
				return err
			},
		})
	}
	runErrors := sup.Run()

	if len(runErrors) > 0 {
		sup.Logger.Printf("collector has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Telepresence connector %s is done.", client.DisplayVersion())
	return nil
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*version.VersionInfo, error) {
	return &version.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Status(_ context.Context, _ *empty.Empty) (*rpc.ConnectorStatus, error) {
	return s.status(s.p), nil
}

func (s *service) Connect(_ context.Context, cr *rpc.ConnectRequest) (*rpc.ConnectInfo, error) {
	return s.connect(s.p, cr), nil
}

func (s *service) CreateIntercept(_ context.Context, ir *manager.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	return s.trafficMgr.addIntercept(s.p, ir)
}

func (s *service) RemoveIntercept(_ context.Context, rr *manager.RemoveInterceptRequest2) (*rpc.InterceptResult, error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	_, err := s.trafficMgr.removeIntercept(s.p, rr.Name)
	if err != nil {
		return nil, err
	}
	return &rpc.InterceptResult{}, nil
}

func (s *service) AvailableIntercepts(_ context.Context, _ *empty.Empty) (*manager.AgentInfoSnapshot, error) {
	if !s.trafficMgr.IsOkay() {
		return &manager.AgentInfoSnapshot{}, nil
	}
	return s.trafficMgr.agentInfoSnapshot(), nil
}

func (s *service) ListIntercepts(_ context.Context, _ *empty.Empty) (*manager.InterceptInfoSnapshot, error) {
	if !s.trafficMgr.IsOkay() {
		return &manager.InterceptInfoSnapshot{}, nil
	}
	return s.trafficMgr.interceptInfoSnapshot(), nil
}

func (s *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.p.Supervisor().Shutdown()
	return &empty.Empty{}, nil
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
func (s *service) runGRPCService(p *supervisor.Process, cancel context.CancelFunc) error {
	p.Log("---")
	p.Logf("Telepresence Connector %s starting...", client.DisplayVersion())
	p.Logf("PID is %d", os.Getpid())
	p.Log("")

	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", client.ConnectorSocketName)
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

// connect the connector to a cluster
func (s *service) connect(p *supervisor.Process, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	reporter := &metriton.Reporter{
		Application:  "telepresence2",
		Version:      client.Version(),
		GetInstallID: func(_ *metriton.Reporter) (string, error) { return cr.InstallId, nil },
		BaseMetadata: map[string]interface{}{"mode": "daemon"},
	}

	if _, err := reporter.Report(p.Context(), map[string]interface{}{"action": "connect"}); err != nil {
		p.Logf("report failed: %+v", err)
	}

	// Sanity checks
	r := &rpc.ConnectInfo{}
	if s.cluster != nil {
		r.Error = rpc.ConnectInfo_ALREADY_CONNECTED
		return r
	}
	if s.bridge != nil {
		r.Error = rpc.ConnectInfo_DISCONNECTING
		return r
	}

	p.Log("Connecting to traffic manager...")
	cluster, err := trackKCluster(p, cr.Context, cr.Namespace, cr.Args)
	if err != nil {
		p.Logf("unable to track k8s cluster: %+v", err)
		r.Error = rpc.ConnectInfo_CLUSTER_FAILED
		r.ErrorText = err.Error()
		s.p.Supervisor().Shutdown()
		return r
	}
	s.cluster = cluster

	/*
		previewHost, err := cluster.getClusterPreviewHostname(p)
		if err != nil {
			p.Logf("get preview URL hostname: %+v", err)
			previewHost = ""
		}
	*/

	p.Logf("Connected to context %s (%s)", s.cluster.Context, s.cluster.server())

	r.ClusterContext = s.cluster.Context
	r.ClusterServer = s.cluster.server()

	tmgr, err := newTrafficManager(p, s.cluster, cr.InstallId, cr.IsCi)
	if err != nil {
		p.Logf("Unable to connect to TrafficManager: %s", err)
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = err.Error()
		if cr.InterceptEnabled {
			// No point in continuing without a traffic manager
			s.p.Supervisor().Shutdown()
		}
		return r
	}
	// tmgr.previewHost = previewHost
	s.trafficMgr = tmgr
	p.Logf("Starting traffic-manager bridge in context %s, namespace %s", cluster.Context, cluster.Namespace)
	br := newBridge(cluster, s.daemon, tmgr.sshPort)
	err = br.start(p)
	if err != nil {
		p.Logf("Failed to start traffic-manager bridge: %s", err.Error())
		r.Error = rpc.ConnectInfo_BRIDGE_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a bridge
		s.p.Supervisor().Shutdown()
		return r
	}
	s.bridge = br
	s.cluster.setBridgeCheck(func() bool {
		return br.check(p)
	})

	if !cr.InterceptEnabled {
		return r
	}

	// Wait for traffic manager to connect
	maxAttempts := 30 * 4 // 30 seconds max wait
	attempts := 0
	p.Log("Waiting for TrafficManager to connect")
	for ; !tmgr.IsOkay() && attempts < maxAttempts; attempts++ {
		if s.trafficMgr.apiErr != nil {
			r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
			r.ErrorText = s.trafficMgr.apiErr.Error()
			// No point in continuing without a traffic manager
			s.p.Supervisor().Shutdown()
			break
		}
		time.Sleep(time.Second / 4)
	}
	if attempts == maxAttempts {
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = "Timeout waiting for traffic manager"
		p.Log(r.ErrorText)
		s.p.Supervisor().Shutdown()
	}
	return r
}

// daemonLogger is a supervisor.Logger implementation that sends log messages to the daemon
type daemonLogger struct {
	stream daemon.Daemon_LoggerClient
}

// Printf implements the supervisor.Logger interface
func (d *daemonLogger) Printf(format string, v ...interface{}) {
	txt := fmt.Sprintf(format, v...)
	err := d.stream.Send(&daemon.LogMessage{Text: txt})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error while sending log message to daemon: %s\nOriginal message was %q\n", err.Error(), txt)
	}
}

// handleSignalsAndShutdown ensures that the connector quits gracefully when receiving a signal
// or when the supervisor wants to shutdown.
func (s *service) handleSignalsAndShutdown(cancel context.CancelFunc) {
	defer s.grpc.GracefulStop()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		select {
		case sig := <-interrupt:
			s.p.Logf("Received signal %s", sig)
			if sig == syscall.SIGHUP {
				if bridge := s.bridge; bridge != nil {
					bridge.restart()
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

	if trafficMgr != nil {
		_ = trafficMgr.clearIntercepts(s.p)
		_ = trafficMgr.Close()
	}
	s.bridge = nil
}
