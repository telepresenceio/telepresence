package main

import (
	"io/ioutil"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	"fmt"

	"github.com/datawire/teleproxy/pkg/supervisor"
	rpc "github.com/gorilla/rpc/v2"
	"github.com/gorilla/rpc/v2/json2"
)

// getRPCServer returns an RPC server that locks around every call
func getRPCServer(p *supervisor.Process) *rpc.Server {
	serverLock := new(sync.Mutex)

	rpcServer := rpc.NewServer()
	rpcServer.RegisterCodec(json2.NewCodec(), "application/json")
	rpcServer.RegisterBeforeFunc(func(_ *rpc.RequestInfo) {
		serverLock.Lock()
	})
	rpcServer.RegisterAfterFunc(func(i *rpc.RequestInfo) {
		p.Logf("RPC call method=%s err=%v", i.Method, i.Error)
		serverLock.Unlock()
	})
	return rpcServer
}

func checkNetOverride() error {
	// Check by doing the equivalent of curl http://teleproxy/api/tables/
	// It's okay to create a new client each time because we don't want to
	// reuse connections.
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(fmt.Sprintf(
		"http://teleproxy%d.cachebust.telepresence.io/api/tables",
		time.Now().Unix(),
	))
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	return nil
}

// DaemonService is the RPC Service used to export Playpen Daemon functionality
// to the client
type DaemonService struct {
	p              *supervisor.Process
	network        Resource
	cluster        Resource
	bridge         Resource
	intercepts     []Resource
	interceptables []string
}

// MakeDaemonService creates a DaemonService object
func MakeDaemonService(p *supervisor.Process) (*DaemonService, error) {
	teleproxy := "/Users/ark3/datawire/bin/pp-teleproxy-darwin-amd64"
	netOverride, err := CheckedRetryingCommand(
		p,
		"netOverride",
		[]string{teleproxy, "-mode", "intercept"},
		&RunAsInfo{},
		checkNetOverride,
	)
	if err != nil {
		return nil, err
	}
	return &DaemonService{
		p:       p,
		network: netOverride,
	}, nil
}

// Status reports the current status of the daemon
func (d *DaemonService) Status(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	res := new(strings.Builder)
	defer func() { reply.Message = res.String() }()

	if !d.network.IsOkay() {
		fmt.Fprintln(res, "Network overrides NOT established")
	}
	if d.cluster == nil {
		fmt.Fprintln(res, "Not connected")
		return nil
	}
	if d.cluster.IsOkay() {
		fmt.Fprintln(res, "Connected")
	} else {
		fmt.Fprintln(res, "Attempting to reconnect...")
	}
	fmt.Fprintf(res, "  Context:       %s (%s)\n", "{context}", "{server}")
	if d.bridge != nil && d.bridge.IsOkay() {
		fmt.Fprintln(res, "  Proxy:         ON (networking to the cluster is enabled)")
	} else {
		fmt.Fprintln(res, "  Proxy:         OFF (attempting to connect...)")
	}
	fmt.Fprintf(res, "  Interceptable: %d deployments\n", len(d.interceptables))
	fmt.Fprintf(res, "  Intercepts:    ? total, %d local\n", len(d.intercepts))
	return nil
}

// Connect the daemon to a cluster
func (d *DaemonService) Connect(_ *http.Request, args *ConnectArgs, reply *StringReply) error {
	cmdArgs := make([]string, 0, 3+len(args.KArgs))
	cmdArgs = append(cmdArgs, "kubectl", "get", "po")
	cmdArgs = append(cmdArgs, args.KArgs...)
	cmd := args.RAI.Command(d.p, cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_, ok := err.(*exec.ExitError)
		if !ok {
			return err
		}
	}
	reply.Message = strings.TrimRightFunc(string(output), unicode.IsSpace)
	return nil
}

// Disconnect from the connected cluster
func (d *DaemonService) Disconnect(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
	return nil
}
