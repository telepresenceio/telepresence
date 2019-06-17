package main

import (
	"net/http"
	"os/exec"
	"strings"
	"unicode"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

// DaemonService is the RPC Service used to export Playpen Daemon functionality
// to the client
type DaemonService struct {
	p *supervisor.Process
}

// Status reports the current status of the daemon
func (d *DaemonService) Status(r *http.Request, args *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
	return nil
}

// Connect the daemon to a cluster
func (d *DaemonService) Connect(r *http.Request, args *ConnectArgs, reply *StringReply) error {
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
func (d *DaemonService) Disconnect(r *http.Request, args *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
	return nil
}
