package main

import "net/http"

// DaemonService is the RPC Service used to export Playpen Daemon functionality
// to the client
type DaemonService struct{}

// Status reports the current status of the daemon
func (d *DaemonService) Status(r *http.Request, args *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
	return nil
}

// Connect the daemon to a cluster
func (d *DaemonService) Connect(r *http.Request, args *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not implemented..."
	return nil
}

// Disconnect from the connected cluster
func (d *DaemonService) Disconnect(r *http.Request, args *EmptyArgs, reply *StringReply) error {
	reply.Message = "Not connected"
	return nil
}
