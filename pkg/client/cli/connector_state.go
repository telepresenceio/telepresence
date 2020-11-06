package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
)

type connectorState struct {
	cmd    *cobra.Command
	cr     *connector.ConnectRequest
	conn   *grpc.ClientConn
	daemon daemon.DaemonClient
	grpc   connector.ConnectorClient
}

// Connect asks the daemon to connect to a cluster
func (cs *connectorState) EnsureState() (bool, error) {
	out := cs.cmd.OutOrStdout()
	if cs.isConnected() {
		fmt.Fprintln(out, "Already connected")
		return false, nil
	}

	for attempt := 0; ; attempt++ {
		dr, err := cs.daemon.Status(context.Background(), &empty.Empty{})
		if err != nil {
			return false, err
		}
		switch dr.Error {
		case daemon.DaemonStatus_UNSPECIFIED:
		case daemon.DaemonStatus_NOT_STARTED:
			return false, daemonIsNotRunning
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

	cs.cr.InstallId = client.NewScout("unused").Reporter.InstallID()

	err := start(client.GetExe(), []string{"connector-foreground"}, false, nil, nil, nil)
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the connector service")
	}

	// TODO: Progress reporting during connect. Either divide into several calls and report completion
	//  of each, or use a stream. Can be made as part of ticket #1334.
	var r *connector.ConnectInfo
	fmt.Fprintln(out, "Connecting to traffic manager...")

	if err = client.WaitUntilSocketAppears("connector", client.ConnectorSocketName, 10*time.Second); err != nil {
		return false, fmt.Errorf("Connector service did not come up!\nTake a look at %s for more information.", client.Logfile)
	}
	err = cs.connect()
	if err != nil {
		return false, err
	}

	r, err = cs.grpc.Connect(context.Background(), cs.cr)
	if err != nil {
		return false, err
	}

	var msg string
	switch r.Error {
	case connector.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(out, "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return true, nil
	case connector.ConnectInfo_ALREADY_CONNECTED:
		fmt.Fprintln(out, "Already connected")
		return false, nil
	case connector.ConnectInfo_DISCONNECTING:
		msg = "Unable to connect while disconnecting"
	case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED, connector.ConnectInfo_CLUSTER_FAILED, connector.ConnectInfo_BRIDGE_FAILED:
		msg = r.ErrorText
	}
	return true, errors.New(msg) // Return true to ensure disconnect
}

func (cs *connectorState) DeactivateState() error {
	if !cs.isConnected() {
		return nil
	}
	out := cs.cmd.OutOrStdout()
	fmt.Fprint(out, "Disconnecting...")
	var err error
	if client.SocketExists(client.ConnectorSocketName) {
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

func newConnectorState(daemon daemon.DaemonClient, cr *connector.ConnectRequest, cmd *cobra.Command) (*connectorState, error) {
	cs := &connectorState{daemon: daemon, cmd: cmd, cr: cr}
	err := assertConnectorStarted()
	if err == nil {
		err = cs.connect()
	}
	return cs, err
}

// isConnected returns true if a connection has been established to the daemon
func (cs *connectorState) isConnected() bool {
	return cs.conn != nil
}

// connect opens the client connection to the daemon.
func (cs *connectorState) connect() (err error) {
	if cs.conn, err = grpc.Dial(client.SocketURL(client.ConnectorSocketName), grpc.WithInsecure()); err == nil {
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
