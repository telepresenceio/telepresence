package state

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BeginLogStream registers the session's single allowed StreamLogs call,
// failing with ResourceExhausted while another is active -- a client makes
// one call per log collection, so anything more is amplification, not use.
// The returned release function must be invoked when the stream ends.
func (cs *ClientSession) BeginLogStream() (func(), error) {
	if !cs.logStreamActive.CompareAndSwap(false, true) {
		return nil, status.Errorf(codes.ResourceExhausted,
			"client session %q already has an active StreamLogs call", cs.sessionID())
	}
	return func() { cs.logStreamActive.Store(false) }, nil
}
