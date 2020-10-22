package client

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

type connectorState struct {
	out    io.Writer
	cr     *rpc.ConnectRequest
	conn   *grpc.ClientConn
	daemon rpc.DaemonClient
	grpc   rpc.ConnectorClient
}

// Connect asks the daemon to connect to a cluster
func (cs *connectorState) EnsureState() (bool, error) {
	if cs.isConnected() {
		fmt.Fprintln(cs.out, "Already connected")
		return false, nil
	}

	for attempt := 0; ; attempt++ {
		dr, err := cs.daemon.Status(context.Background(), &empty.Empty{})
		if err != nil {
			return false, err
		}
		switch dr.Error {
		case rpc.DaemonStatus_UNSPECIFIED:
		case rpc.DaemonStatus_NOT_STARTED:
			return false, daemonIsNotRunning
		case rpc.DaemonStatus_NO_NETWORK:
			if attempt >= 40 {
				return false, errors.New("Unable to connect: Network overrides are not established")
			}
			time.Sleep(250 * time.Millisecond)
			continue
		case rpc.DaemonStatus_PAUSED:
			return false, errors.New("Unable to connect: Network overrides are paused (use 'telepresence resume')")
		}
		break
	}

	cs.cr.InstallId = NewScout("unused").Reporter.InstallID()

	err := start(common.GetExe(), []string{"connector-foreground"}, false, nil, nil, nil)
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the connector service")
	}

	// TODO: Progress reporting during connect. Either divide into several calls and report completion
	//  of each, or use a stream. Can be made as part of ticket #1334.
	var r *rpc.ConnectInfo
	fmt.Fprintf(cs.out, "Connecting to traffic manager in namespace %s...\n", cs.cr.ManagerNs)

	if err = common.WaitUntilSocketAppears("connector", common.ConnectorSocketName, 10*time.Second); err != nil {
		return false, fmt.Errorf("Connector service did not come up!\nTake a look at %s for more information.", common.Logfile)
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
	case rpc.ConnectInfo_UNSPECIFIED:
		fmt.Fprintf(cs.out, "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return true, nil
	case rpc.ConnectInfo_ALREADY_CONNECTED:
		fmt.Fprintln(cs.out, "Already connected")
		return false, nil
	case rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED:
		fmt.Fprintf(cs.out, `Connected to context %s (%s)

Unable to connect to the traffic manager.
The intercept feature will not be available.
Error was: %s
`, r.ClusterContext, r.ClusterServer, r.ErrorText)

		// The connect is considered a success. There's still a cluster connection and bridge.
		// TODO: This is obviously not true for the run subcommand.
		return true, nil
	case rpc.ConnectInfo_DISCONNECTING:
		msg = "Unable to connect while disconnecting"
	case rpc.ConnectInfo_CLUSTER_FAILED, rpc.ConnectInfo_BRIDGE_FAILED:
		msg = r.ErrorText
	}
	return false, errors.New(msg)
}

func (cs *connectorState) DeactivateState() error {
	if !cs.isConnected() {
		return nil
	}
	fmt.Fprint(cs.out, "Disconnecting...")
	var err error
	if common.SocketExists(common.ConnectorSocketName) {
		_, err = cs.grpc.Quit(context.Background(), &empty.Empty{})
	}
	cs.disconnect()
	if err == nil {
		err = common.WaitUntilSocketVanishes("connector", common.ConnectorSocketName, 5*time.Second)
	}
	if err == nil {
		fmt.Fprintln(cs.out, "done")
	} else {
		fmt.Fprintln(cs.out, "failed")
	}
	return err
}

func newConnectorState(daemon rpc.DaemonClient, cr *rpc.ConnectRequest, out io.Writer) (*connectorState, error) {
	cs := &connectorState{daemon: daemon, out: out, cr: cr}
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
	if cs.conn, err = grpc.Dial(common.SocketURL(common.ConnectorSocketName), grpc.WithInsecure()); err == nil {
		cs.grpc = rpc.NewConnectorClient(cs.conn)
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
