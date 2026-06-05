package usg

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func withManagerProducer(t *testing.T) (context.Context, Sink) {
	t.Helper()
	sink := NewMemSink()
	p := newProducer(SourceManager, "test-install", "v0.0.0-test", sink)
	return WithProducer(context.Background(), p), sink
}

func TestReportServerError(t *testing.T) {
	const method = "/telepresence.manager.Manager/CreateIntercept"

	t.Run("manager reports reportable error", func(t *testing.T) {
		ctx, sink := withManagerProducer(t)
		ReportServerError(ctx, method, errcat.User.New(errors.New("boom")))

		got := sink.Drain(0)
		if len(got) != 1 {
			t.Fatalf("want 1 report, got %d", len(got))
		}
		e := got[0].Entries
		if got[0].Topic != ServerErrorTopic {
			t.Errorf("topic: got %q, want %q", got[0].Topic, ServerErrorTopic)
		}
		if e["method"] != method {
			t.Errorf("method: got %q", e["method"])
		}
		if e["error.cat"] != "user" {
			t.Errorf("error.cat: got %q", e["error.cat"])
		}
		if e["error.chain"] == "" {
			t.Error("error.chain should be set")
		}
	})

	t.Run("benign code is not reported", func(t *testing.T) {
		ctx, sink := withManagerProducer(t)
		ReportServerError(ctx, method, status.Error(codes.NotFound, "missing"))
		if n := sink.Len(); n != 0 {
			t.Errorf("benign code should not report, got %d", n)
		}
	})

	t.Run("client producer does not report", func(t *testing.T) {
		ctx, sink := withTestProducer(t) // SourceClient
		ReportServerError(ctx, method, errors.New("boom"))
		if n := sink.Len(); n != 0 {
			t.Errorf("client producer should not report server errors, got %d", n)
		}
	})

	t.Run("nil error is a no-op", func(t *testing.T) {
		ctx, sink := withManagerProducer(t)
		ReportServerError(ctx, method, nil)
		if n := sink.Len(); n != 0 {
			t.Errorf("nil error should not report, got %d", n)
		}
	})

	t.Run("no producer is a no-op", func(t *testing.T) {
		// Must not panic when reporting is disabled.
		ReportServerError(context.Background(), method, errors.New("boom"))
	})
}

func TestIsBenignCode(t *testing.T) {
	benign := []codes.Code{
		codes.OK, codes.Canceled, codes.DeadlineExceeded,
		codes.NotFound, codes.AlreadyExists, codes.Unimplemented,
	}
	for _, c := range benign {
		if !isBenignCode(c) {
			t.Errorf("%s should be benign", c)
		}
	}
	reportable := []codes.Code{
		codes.Internal, codes.Unavailable, codes.FailedPrecondition,
		codes.PermissionDenied, codes.ResourceExhausted, codes.Unknown,
	}
	for _, c := range reportable {
		if isBenignCode(c) {
			t.Errorf("%s should be reportable", c)
		}
	}
}
