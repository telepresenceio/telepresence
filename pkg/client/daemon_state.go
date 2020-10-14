package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/datawire/telepresence2/pkg/rpc"
)

type daemonState struct {
	out      io.Writer
	dns      string
	fallback string
	conn     *grpc.ClientConn
	grpc     rpc.DaemonClient
}

func newDaemonState(out io.Writer, dns, fallback string) (*daemonState, error) {
	ds := &daemonState{out: out, dns: dns, fallback: fallback}
	err := assertDaemonStarted()
	if err == nil {
		err = ds.connect()
	}
	return ds, err
}

func (ds *daemonState) EnsureState() (bool, error) {
	if ds.isConnected() {
		return false, nil
	}
	quitLegacyDaemon(ds.out)

	fmt.Fprintln(ds.out, "Launching Edge Control Daemon", common.DisplayVersion())

	err := runAsRoot(common.GetExe(), []string{"daemon-foreground", ds.dns, ds.fallback})
	if err != nil {
		return false, errors.Wrap(err, "failed to launch the server")
	}

	if err = common.WaitUntilSocketAppears("daemon", common.DaemonSocketName, 10*time.Second); err != nil {
		return false, fmt.Errorf("Daemon service did not come up!\nTake a look at %s for more information.", common.Logfile)
	}
	err = ds.connect()
	return err == nil, err
}

func (ds *daemonState) DeactivateState() error {
	if !ds.isConnected() {
		return nil
	}
	fmt.Fprint(ds.out, "Edge Control Daemon quitting...")
	_, err := ds.grpc.Quit(context.Background(), &rpc.Empty{})
	ds.disconnect()
	if err == nil {
		err = common.WaitUntilSocketVanishes("daemon", common.DaemonSocketName, 5*time.Second)
	}
	if err == nil {
		fmt.Fprintln(ds.out, "done")
	} else {
		fmt.Fprintln(ds.out)
	}
	return err
}

// isConnected returns true if a connection has been established to the daemon
func (ds *daemonState) isConnected() bool {
	return ds.conn != nil
}

// connect opens the client connection to the daemon.
func (ds *daemonState) connect() (err error) {
	if ds.conn, err = grpc.Dial(common.SocketURL(common.DaemonSocketName), grpc.WithInsecure()); err == nil {
		ds.grpc = rpc.NewDaemonClient(ds.conn)
	}
	return
}

// disconnect closes the client connection to the daemon.
func (ds *daemonState) disconnect() {
	conn := ds.conn
	ds.conn = nil
	ds.grpc = nil
	if conn != nil {
		conn.Close()
	}
}

func (ds *daemonState) version() (int, string, error) {
	vi, err := ds.grpc.Version(context.Background(), &rpc.Empty{})
	if err != nil {
		return 0, "", err
	}
	return int(vi.APIVersion), vi.Version, nil
}

const legacySocketName = "/var/run/common.socket"

// quitLegacyDaemon ensures that an older version of the daemon quits and removes the old socket.
func quitLegacyDaemon(out io.Writer) {
	if !common.SocketExists(legacySocketName) {
		return // no legacy daemon is running
	}
	if conn, err := net.Dial("unix", legacySocketName); err == nil {
		defer conn.Close()

		io.WriteString(conn, `{"Args": ["edgectl", "quit"], "APIVersion": 1}`)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			fmt.Fprintf(out, "Legacy daemon: %s\n", scanner.Text())
		}
	}
}
