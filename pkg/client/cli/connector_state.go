package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/telepresence2/rpc/v2/connector"
	"github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/filelocation"
)

type connectorState struct {
	*sessionInfo
	daemonClient daemon.DaemonClient

	connectorConn   *grpc.ClientConn
	connectorClient connector.ConnectorClient
	managerClient   manager.ManagerClient

	info *connector.ConnectInfo
}

func NewConnectorState(sessionInfo *sessionInfo, daemonClient daemon.DaemonClient) *connectorState {
	return &connectorState{
		sessionInfo:  sessionInfo,
		daemonClient: daemonClient,
	}
}

// Connect asks the daemon to connect to a cluster
func (cs *connectorState) EnsureState() (bool, error) {
	if cs.isConnected() {
		return false, cs.setConnectInfo()
	}

	for attempt := 0; ; attempt++ {
		dr, err := cs.daemonClient.Status(cs.cmd.Context(), &empty.Empty{})
		if err != nil {
			return false, err
		}
		switch dr.Error {
		case daemon.DaemonStatus_UNSPECIFIED:
		case daemon.DaemonStatus_NOT_STARTED:
			return false, errDaemonIsNotRunning
		case daemon.DaemonStatus_NO_NETWORK:
			if attempt >= 40 {
				return false, errors.New("Unable to connect: Network overrides are not established")
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}
		break
	}

	err := start(client.GetExe(), []string{"connector-foreground"}, false, nil, nil, nil)
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the connector service")
	}
	fmt.Fprintln(cs.cmd.OutOrStdout(), "Connecting to traffic manager...")

	if err = client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
		logDir, _ := filelocation.AppUserLogDir(cs.cmd.Context())
		return false, fmt.Errorf("connector service did not start (see %q for more info)", filepath.Join(logDir, "connector.log"))
	}
	err = cs.connect()
	if err != nil {
		return true, err
	}
	return true, cs.setConnectInfo()
}

func (cs *connectorState) setConnectInfo() error {
	installID := client.NewScout("unused").Reporter.InstallID()
	cr := &connector.ConnectRequest{
		KubeFlags:        cs.kubeFlagMap(),
		InstallId:        installID,
		MappedNamespaces: mappedNamespaces,
	}

	// We first connect to the kubernetes cluster
	rClusterInfo, err := cs.connectorClient.ConnectCluster(cs.cmd.Context(), cr)
	if err != nil {
		return err
	}

	var msg string
	switch rClusterInfo.Error {
	case connector.ClusterInfo_UNSPECIFIED:
		// If the cluster seems large on the initial connect, then we alert the
		// user to a flag that should help speed things up if the connect is
		// slow for them.
		sumClusterObjects := rClusterInfo.Pods + rClusterInfo.Services
		// These thresholds likely needs some tuning
		if sumClusterObjects > 50 || rClusterInfo.Namespaces > 10 {
			msg := "Your cluster seems pretty big, so this could take some time. " +
				"We recommend connecting with the --mapped-namespaces flag and a " +
				"list of namespaces you want to be able to intercept in to speed " +
				"it up!\n"
			fmt.Fprint(cs.cmd.OutOrStdout(), msg)
		}
	case connector.ClusterInfo_DISCONNECTING:
		msg = "Unable to connect while disconnecting"
	case connector.ClusterInfo_MUST_RESTART:
		msg = "Cluster configuration changed, please quit telepresence and reconnect"
	case connector.ClusterInfo_CLUSTER_FAILED:
		msg = rClusterInfo.ErrorText
	case connector.ClusterInfo_ALREADY_CONNECTED:
	}
	if msg != "" {
		return errors.New(msg) // Return true to ensure disconnect
	}

	// Now we connect to the traffic manager
	r, err := cs.connectorClient.Connect(cs.cmd.Context(), cr)
	if err != nil {
		return err
	}
	cs.info = r

	switch r.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(cs.cmd.OutOrStdout(), "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return nil
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_BRIDGE_FAILED:
		msg = r.ErrorText
	}
	return errors.New(msg) // Return true to ensure disconnect
}

func (cs *connectorState) DeactivateState() error {
	if !cs.isConnected() {
		return nil
	}
	out := cs.cmd.OutOrStdout()
	fmt.Fprint(out, "Disconnecting...")
	var err error
	if client.SocketExists(client.ConnectorSocketName) {
		// using context.Background() here since it's likely that the
		// command context has been cancelled.
		_, err = cs.connectorClient.Quit(context.Background(), &empty.Empty{})
	}
	cs.disconnect()
	if err == nil {
		err = client.WaitUntilSocketVanishes("connector", client.ConnectorSocketName, 5*time.Second)
	}
	if err == nil {
		fmt.Fprintln(out, "done")
	} else {
		fmt.Fprintln(out, "failed")
	}
	return err
}

func assertConnectorStarted() error {
	if client.SocketExists(client.ConnectorSocketName) {
		return nil
	}
	return errConnectorIsNotRunning
}

// isConnected returns true if a connection has been established to the daemon
func (cs *connectorState) isConnected() bool {
	return cs.connectorConn != nil
}

// connect opens the client connection to the daemon.
func (cs *connectorState) connect() (err error) {
	if cs.connectorConn, err = client.DialSocket(cs.cmd.Context(), client.ConnectorSocketName); err == nil {
		cs.connectorClient = connector.NewConnectorClient(cs.connectorConn)
		cs.managerClient = manager.NewManagerClient(cs.connectorConn)
	}
	return
}

// disconnect closes the client connection to the daemon.
func (cs *connectorState) disconnect() {
	conn := cs.connectorConn
	cs.connectorConn = nil
	cs.connectorClient = nil
	cs.managerClient = nil
	if conn != nil {
		conn.Close()
	}
}
