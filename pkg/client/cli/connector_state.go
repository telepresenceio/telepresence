package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
)

type connectorState struct {
	*sessionInfo
	conn   *grpc.ClientConn
	daemon daemon.DaemonClient
	grpc   connector.ConnectorClient
	info   *connector.ConnectInfo
}

// Connect asks the daemon to connect to a cluster
func (cs *connectorState) EnsureState() (bool, error) {
	if cs.isConnected() {
		return false, cs.setConnectInfo()
	}

	for attempt := 0; ; attempt++ {
		dr, err := cs.daemon.Status(cs.cmd.Context(), &empty.Empty{})
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
		case daemon.DaemonStatus_PAUSED:
			return false, errors.New("Unable to connect: Network overrides are paused (use 'telepresence resume')")
		}
		break
	}

	err := start(client.GetExe(), []string{"connector-foreground"}, false, nil, nil, nil)
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the connector service")
	}
	fmt.Fprintln(cs.cmd.OutOrStdout(), "Connecting to traffic manager...")

	if err = client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
		return false, fmt.Errorf("connector service did not start (see %s for more info)", client.Logfile)
	}
	err = cs.connect()
	if err != nil {
		return true, err
	}
	return true, cs.setConnectInfo()
}

func (cs *connectorState) setConnectInfo() error {
	r, err := cs.grpc.Connect(cs.cmd.Context(), &connector.ConnectRequest{
		Context:          cs.context,
		Namespace:        cs.namespace,
		InstallId:        client.NewScout("unused").Reporter.InstallID(),
		IsCi:             cs.isCI,
		InterceptEnabled: true,
	})
	if err != nil {
		return err
	}
	cs.info = r

	var msg string
	switch r.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(cs.cmd.OutOrStdout(), "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		return nil
	case connector.ConnectInfo_DISCONNECTING:
		msg = "Unable to connect while disconnecting"
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_BRIDGE_FAILED:
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
		_, err = cs.grpc.Quit(context.Background(), &empty.Empty{})
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
	return cs.conn != nil
}

// connect opens the client connection to the daemon.
func (cs *connectorState) connect() (err error) {
	if cs.conn, err = client.DialSocket(client.ConnectorSocketName); err == nil {
		cs.grpc = connector.NewConnectorClient(cs.conn)
	}
	return
}

// disconnect closes the client connection to the daemon.
func (cs *connectorState) disconnect() {
	conn := cs.conn
	cs.conn = nil
	cs.grpc = nil
	if conn != nil {
		conn.Close()
	}
}
