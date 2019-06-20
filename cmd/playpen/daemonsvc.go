package main

import (
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"unicode"

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

// DaemonService is the RPC Service used to export Playpen Daemon functionality
// to the client
type DaemonService struct {
	p *supervisor.Process
}

// Status reports the current status of the daemon
func (d *DaemonService) Status(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
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
