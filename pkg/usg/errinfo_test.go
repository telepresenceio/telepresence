package usg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// codedError mimics the shape of pkg/grpc.StructuredError: it exposes the gRPC
// code via GetCode() but implements neither GRPCStatus() nor Unwrap(), so it is
// invisible to status.FromError. This is the form errors take after crossing
// the daemon->CLI boundary.
type codedError struct{ code codes.Code }

func (e *codedError) Error() string       { return "coded" }
func (e *codedError) GetCode() codes.Code { return e.code }

func TestAnalyzeError(t *testing.T) {
	plain := errors.New("plain failure")

	cases := []struct {
		name  string
		err   error
		cat   string
		chain string
		grpc  string
		http  string
		k8s   string
	}{
		{
			name:  "uncategorized",
			err:   plain,
			cat:   "unknown",
			chain: "*errors.errorString",
		},
		{
			name:  "categorized wrapper stripped",
			err:   errcat.User.New(plain),
			cat:   "user",
			chain: "*errors.errorString",
		},
		{
			name:  "fmt and errcat wrappers stripped",
			err:   fmt.Errorf("ctx: %w", errcat.User.New(plain)),
			cat:   "user",
			chain: "*errors.errorString",
		},
		{
			name:  "grpc status",
			err:   status.Error(codes.Unavailable, "down"),
			cat:   "unknown",
			chain: typeName(status.Error(codes.Unavailable, "down")),
			grpc:  "Unavailable",
		},
		{
			name:  "grpc code via GetCode",
			err:   &codedError{code: codes.PermissionDenied},
			cat:   "unknown",
			chain: "*usg.codedError",
			grpc:  "PermissionDenied",
		},
		{
			name:  "k8s not found",
			err:   apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "x"),
			cat:   "unknown",
			chain: "*errors.StatusError",
			http:  "404",
			k8s:   "NotFound",
		},
		{
			name:  "sentinel deadline",
			err:   context.DeadlineExceeded,
			cat:   "unknown",
			chain: "context.DeadlineExceeded",
		},
		{
			name:  "sentinel wrapped keeps wrapper-free name",
			err:   fmt.Errorf("waiting: %w", io.EOF),
			cat:   "unknown",
			chain: "io.EOF",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := analyzeError(c.err)
			if got.category != c.cat {
				t.Errorf("category: want %q, got %q", c.cat, got.category)
			}
			if got.chain != c.chain {
				t.Errorf("chain: want %q, got %q", c.chain, got.chain)
			}
			if got.grpcCode != c.grpc {
				t.Errorf("grpc: want %q, got %q", c.grpc, got.grpcCode)
			}
			if got.httpCode != c.http {
				t.Errorf("http: want %q, got %q", c.http, got.httpCode)
			}
			if got.k8sReason != c.k8s {
				t.Errorf("k8s: want %q, got %q", c.k8s, got.k8sReason)
			}
		})
	}
}

// TestErrInfo_AddTo verifies that addTo emits the always-present keys and omits
// the optional code keys when their fields are empty.
func TestErrInfo_AddTo(t *testing.T) {
	ctx, sink := withTestProducer(t)

	r := New(ctx, "cmd.test")
	analyzeError(status.Error(codes.Unavailable, "down")).addTo(r)
	r.Send()

	got := sink.Drain(0)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d", len(got))
	}
	e := got[0].Entries
	if e["error.cat"] != "unknown" {
		t.Errorf("error.cat: got %q", e["error.cat"])
	}
	if e["error.grpc"] != "Unavailable" {
		t.Errorf("error.grpc: got %q", e["error.grpc"])
	}
	if e["error.chain"] == "" {
		t.Error("error.chain should be set")
	}
	if _, ok := e["error.http"]; ok {
		t.Errorf("error.http should be omitted, got %q", e["error.http"])
	}
	if _, ok := e["error.k8s"]; ok {
		t.Errorf("error.k8s should be omitted, got %q", e["error.k8s"])
	}
}

// TestAnalyzeError_Chain verifies that a multi-frame chain of distinct typed
// errors is rendered outer->inner with structural wrappers removed.
func TestAnalyzeError_Chain(t *testing.T) {
	leaf := errors.New("leaf")
	mid := &wrapperErr{msg: "mid", wrapped: leaf}
	outer := fmt.Errorf("outer: %w", mid)

	got := analyzeError(outer).chain
	want := "*usg.wrapperErr>*errors.errorString"
	if got != want {
		t.Errorf("chain: want %q, got %q", want, got)
	}
}

// TestAnalyzeError_JoinBranches verifies that a joined (multi-) error renders
// its branches as "|"-separated alternatives.
func TestAnalyzeError_JoinBranches(t *testing.T) {
	joined := errors.Join(errors.New("a"), &wrapperErr{msg: "b"})
	got := analyzeError(joined).chain
	want := "*errors.errorString|*usg.wrapperErr"
	if got != want {
		t.Errorf("chain: want %q, got %q", want, got)
	}
}

// wrapperErr is a typed error used to exercise chain rendering; unlike
// fmt.wrapError it is not on the skip list, so it contributes its own token.
type wrapperErr struct {
	msg     string
	wrapped error
}

func (e *wrapperErr) Error() string { return e.msg }
func (e *wrapperErr) Unwrap() error { return e.wrapped }

func typeName(v any) string { return fmt.Sprintf("%T", v) }
