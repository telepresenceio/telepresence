package connector

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/datawire/ambassador/pkg/dgroup"
	"github.com/datawire/ambassador/pkg/dlog"
	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh/terminal"
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
	daemon       daemon.DaemonClient
	daemonLogger daemonLogger
	cluster      *k8sCluster
	bridge       *bridge
	trafficMgr   *trafficManager
	grpc         *grpc.Server
	callCtx      context.Context
	cancel       func()
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

func (s *service) callContext(_ context.Context) context.Context {
	return s.callCtx
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*version.VersionInfo, error) {
	return &version.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Status(c context.Context, _ *empty.Empty) (*rpc.ConnectorStatus, error) {
	return s.status(s.callContext(c)), nil
}

func (s *service) Connect(c context.Context, cr *rpc.ConnectRequest) (*rpc.ConnectInfo, error) {
	return s.connect(s.callContext(c), cr), nil
}

func (s *service) CreateIntercept(c context.Context, ir *manager.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	return s.trafficMgr.addIntercept(s.callContext(c), ir)
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (*rpc.InterceptResult, error) {
	ie, is := s.interceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	_, err := s.trafficMgr.removeIntercept(s.callContext(c), rr.Name)
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
	s.cancel()
	return &empty.Empty{}, nil
}

// daemonLogger is an io.Writer implementation that sends data to the daemon logger
type daemonLogger struct {
	stream daemon.Daemon_LoggerClient
}

func (d *daemonLogger) Write(data []byte) (n int, err error) {
	err = d.stream.Send(&daemon.LogMessage{Text: data})
	return len(data), err
}

// connect the connector to a cluster
func (s *service) connect(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	reporter := &metriton.Reporter{
		Application:  "telepresence2",
		Version:      client.Version(),
		GetInstallID: func(_ *metriton.Reporter) (string, error) { return cr.InstallId, nil },
		BaseMetadata: map[string]interface{}{"mode": "daemon"},
	}

	if _, err := reporter.Report(c, map[string]interface{}{"action": "connect"}); err != nil {
		dlog.Errorf(c, "report failed: %+v", err)
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

	dlog.Info(c, "Connecting to traffic manager...")
	cluster, err := trackKCluster(c, cr.Context, cr.Namespace, cr.Args)
	if err != nil {
		dlog.Errorf(c, "unable to track k8s cluster: %+v", err)
		r.Error = rpc.ConnectInfo_CLUSTER_FAILED
		r.ErrorText = err.Error()
		s.cancel()
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

	dlog.Infof(c, "Connected to context %s (%s)", s.cluster.Context, s.cluster.server())

	r.ClusterContext = s.cluster.Context
	r.ClusterServer = s.cluster.server()

	tmgr, err := newTrafficManager(c, s.cluster, cr.InstallId, cr.IsCi)
	if err != nil {
		dlog.Errorf(c, "Unable to connect to TrafficManager: %s", err)
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = err.Error()
		if cr.InterceptEnabled {
			// No point in continuing without a traffic manager
			s.cancel()
		}
		return r
	}
	// tmgr.previewHost = previewHost
	s.trafficMgr = tmgr
	dlog.Infof(c, "Starting traffic-manager bridge in context %s, namespace %s", cluster.Context, cluster.Namespace)
	br := newBridge(cluster, s.daemon, tmgr.sshPort)
	err = br.start(c)
	if err != nil {
		dlog.Errorf(c, "Failed to start traffic-manager bridge: %s", err.Error())
		r.Error = rpc.ConnectInfo_BRIDGE_FAILED
		r.ErrorText = err.Error()
		// No point in continuing without a bridge
		s.cancel()
		return r
	}
	s.bridge = br
	s.cluster.setBridgeCheck(func() bool {
		return br.check(c)
	})

	if !cr.InterceptEnabled {
		return r
	}

	// Wait for traffic manager to connect
	maxAttempts := 30 * 4 // 30 seconds max wait
	attempts := 0
	dlog.Info(c, "Waiting for TrafficManager to connect")
	for ; !tmgr.IsOkay() && attempts < maxAttempts; attempts++ {
		if s.trafficMgr.apiErr != nil {
			r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
			r.ErrorText = s.trafficMgr.apiErr.Error()
			// No point in continuing without a traffic manager
			s.cancel()
			break
		}
		time.Sleep(time.Second / 4)
	}
	if attempts == maxAttempts {
		r.Error = rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED
		r.ErrorText = "Timeout waiting for traffic manager"
		dlog.Error(c, r.ErrorText)
		s.cancel()
	}
	return r
}

// setUpLogging connects to the daemon logger
func (s *service) setUpLogging(c context.Context) (context.Context, error) {
	var err error
	s.daemonLogger.stream, err = s.daemon.Logger(c)
	if err != nil {
		return nil, err
	}

	logger := logrus.StandardLogger()
	logger.Out = &s.daemonLogger
	loggingToTerminal := terminal.IsTerminal(int(os.Stdout.Fd()))
	if loggingToTerminal {
		logger.Formatter = client.NewFormatter("15:04:05")
	} else {
		logger.Formatter = client.NewFormatter("2006/01/02 15:04:05")
	}
	logger.Level = logrus.DebugLevel
	return dlog.WithLogger(c, dlog.WrapLogrus(logger)), nil
}

// run is the main function when executing as the connector
func run(init bool) (err error) {
	var listener net.Listener
	defer func() {
		if listener != nil {
			_ = listener.Close()
		}
		_ = os.Remove(client.ConnectorSocketName)
	}()

	// Listen on unix domain socket
	listener, err = net.Listen("unix", client.ConnectorSocketName)
	if err != nil {
		return errors.Wrap(err, "listen")
	}

	g := dgroup.NewGroup(context.Background(), dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true})

	g.Go("connector", func(c context.Context) error {
		// establish a connection to the daemon gRPC service
		conn, err := client.DialSocket(client.DaemonSocketName)
		if err != nil {
			return err
		}
		defer conn.Close()
		s := &service{daemon: daemon.NewDaemonClient(conn), grpc: grpc.NewServer()}
		rpc.RegisterConnectorServer(s.grpc, s)

		c, err = s.setUpLogging(c)
		if err != nil {
			return err
		}

		dlog.Info(c, "---")
		dlog.Infof(c, "Telepresence Connector %s starting...", client.DisplayVersion())
		dlog.Infof(c, "PID is %d", os.Getpid())
		dlog.Info(c, "")

		c, s.cancel = context.WithCancel(c)
		s.callCtx = c
		sg := dgroup.NewGroup(c, dgroup.GroupConfig{})
		sg.Go("teardown", s.handleShutdown)
		if init {
			sg.Go("debug-init", func(c context.Context) error {
				_, err = s.Connect(c, &rpc.ConnectRequest{InstallId: "dummy-id"})
				return err
			})
		}

		err = s.grpc.Serve(listener)
		listener = nil
		if err != nil {
			dlog.Error(c, err.Error())
		}
		return err
	})
	return g.Wait()
}

// handleShutdown ensures that the connector quits gracefully when receiving a signal
// or when the context is cancelled.
func (s *service) handleShutdown(c context.Context) error {
	defer s.grpc.GracefulStop()

	<-c.Done()
	dlog.Info(c, "Shutting down")

	cluster := s.cluster
	if cluster == nil {
		return nil
	}
	s.cluster = nil
	trafficMgr := s.trafficMgr

	s.trafficMgr = nil

	defer cluster.Close()

	if trafficMgr != nil {
		_ = trafficMgr.clearIntercepts(context.Background())
		_ = trafficMgr.Close()
	}
	s.bridge = nil
	return nil
}
