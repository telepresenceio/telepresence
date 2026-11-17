package daemon

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

const rootDaemonInUseMessage = "the root daemon is serving another session"

// ErrRootDaemonInUse is the error a root daemon returns to a caller that isn't the owner of its current session.
func ErrRootDaemonInUse() error {
	return status.Error(codes.FailedPrecondition, rootDaemonInUseMessage)
}

// IsRootDaemonInUse returns true if err is, or wraps, the error returned by ErrRootDaemonInUse.
func IsRootDaemonInUse(err error) bool {
	var gs interface{ GRPCStatus() *status.Status }
	if !errors.As(err, &gs) {
		return false
	}
	st := gs.GRPCStatus()
	return st.Code() == codes.FailedPrecondition && st.Message() == rootDaemonInUseMessage
}

// QuitRootDaemon calls Quit on the root daemon. The root daemon refuses the call while it still serves
// a session, and it ends that session when it notices that the caller's previous connection has closed.
// This can take a moment, so a refusal is retried for a short time.
func QuitRootDaemon(ctx context.Context, rd rpc.DaemonClient) (qr *rpc.QuitResponse, err error) {
	const retryFor = 2 * time.Second
	deadline := time.Now().Add(retryFor)
	for {
		if qr, err = rd.Quit(ctx, &emptypb.Empty{}); err == nil || !IsRootDaemonInUse(err) || !time.Now().Before(deadline) {
			return qr, err
		}
		select {
		case <-ctx.Done():
			return qr, err
		case <-time.After(50 * time.Millisecond):
		}
	}
}
