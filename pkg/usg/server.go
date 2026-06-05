package usg

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServerErrorTopic is the topic used for traffic-manager RPC failure reports.
const ServerErrorTopic = "manager.rpc"

// ReportServerError reports a failed gRPC server call as a usage event. It is
// meant to be called from the gRPC error interceptor, where the raw handler
// error is still available before it is flattened for the wire, so the full
// type chain can be captured.
//
// It is a no-op when:
//   - reporting is disabled (no producer on ctx),
//   - the producer is not a manager — the user daemon shares the gRPC server
//     code, and its failures are already reported per-command by the CLI, so
//     reporting here too would double-count,
//   - err is nil or carries a benign/expected gRPC code (see isBenignCode).
//
// method is the fully-qualified RPC method (grpc info.FullMethod); it is a
// code-defined constant and safe to report. No error message text is recorded —
// only the analyzeError summary (category, type chain, status codes).
func ReportServerError(ctx context.Context, method string, err error) {
	if err == nil {
		return
	}
	if p := producerFromContext(ctx); p == nil || p.source != SourceManager {
		return
	}
	if isBenignCode(status.Code(err)) {
		return
	}
	r := New(ctx, ServerErrorTopic)
	r.Add("method", method)
	analyzeError(err).addTo(r)
	r.Send()
}

// isBenignCode reports whether a gRPC code represents a routine or expected
// outcome not worth reporting: success, client cancellation/timeout, and the
// lookup/negotiation codes that occur during normal operation. status.Code
// maps a nil error to OK and the context errors to Canceled/DeadlineExceeded,
// so raw client disconnects are filtered here too.
func isBenignCode(c codes.Code) bool {
	switch c {
	case codes.OK,
		codes.Canceled,
		codes.DeadlineExceeded,
		codes.NotFound,
		codes.AlreadyExists,
		codes.Unimplemented:
		return true
	default:
		return false
	}
}
