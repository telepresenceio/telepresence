// Package usg implements anonymous usage reporting for Telepresence.
//
// A Report is a small, structured message bound to a context.Context. The
// typical lifecycle is:
//
//	r := usg.New(ctx, "cmd.connect")
//	r.Add("flag.docker", "true")
//	...
//	r.Send()                            // returns immediately
//
// Or, when there is nothing to add between construction and send:
//
//	usg.Quick(ctx, "cmd.version")
//
// Reports are enqueued in a FIFO sink (on disk for clients, in memory for the
// traffic-manager). A background sender drains the sink and ships reports over
// gRPC to a configured collector. The package guarantees:
//
//   - Send is non-blocking. Construction and Send never delay the caller.
//   - Reporting can be disabled via config. When disabled, no producer is
//     installed on the context, New() returns nil, and methods on a nil
//     Report are no-ops, so nothing is buffered or sent.
//   - Problems are logged at trace level only, never warning or error.
//
// Sensitive data is the caller's responsibility. This package performs no
// scrubbing or validation of entry values — anything passed to Add is sent
// verbatim. Do not pass cluster, namespace, workload, address, or any
// user-derived strings. Only pass tool-intrinsic values (flag names, bounded
// flag values, fixed identifiers).
package usg

import (
	"context"
	"runtime"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// Source identifies the kind of process producing a report. It is not an
// identifier; it just disambiguates topics that exist on both sides.
type Source string

const (
	SourceClient  Source = "client"
	SourceManager Source = "manager"
)

// Report is an in-progress usage event. It is safe to use a nil *Report; all
// methods are no-ops in that case, which is how disabled reporting is implemented.
type Report struct {
	ctx context.Context
	pb  *usgrpc.UsageReport
}

// New starts a new report bound to ctx. Returns nil if reporting is disabled
// (no producer installed on ctx). A nil Report's methods are all no-ops, so
// callers do not need to nil-check the result.
//
// Envelope fields are truncated to the per-field budgets a collector must
// accept (see Max*Len constants). This is almost never triggered in practice
// — installation IDs are UUIDs, topics are code-defined constants, etc. —
// but keeps a stray oversize value from getting the whole batch rejected.
func New(ctx context.Context, topic string) *Report {
	p := producerFromContext(ctx)
	if p == nil {
		return nil
	}
	return &Report{
		ctx: ctx,
		pb: &usgrpc.UsageReport{
			InstallationId: truncate(p.installationID, MaxInstallationIDLen),
			Source:         truncate(string(p.source), MaxSourceLen),
			Topic:          truncate(topic, MaxTopicLen),
			Version:        truncate(p.version, MaxVersionLen),
			Os:             truncate(runtime.GOOS, MaxOSLen),
			Arch:           truncate(runtime.GOARCH, MaxArchLen),
			Timestamp:      timestamppb.New(time.Now().UTC()),
			Entries:        map[string]string{},
		},
	}
}

// truncate trims s to at most max bytes. Truncation is byte-wise, not
// rune-wise — the collector's limits are byte budgets and the values we send
// are ASCII in practice.
func truncate(s string, limit int) string {
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// Add appends a key/value entry to the report. The value is stored verbatim;
// it is the caller's responsibility to ensure it carries no sensitive data.
// Calls on a nil Report are no-ops.
//
// To stay within the per-report budget a collector must accept, oversized
// keys and values are truncated, and entries beyond MaxEntryCount are
// silently dropped except when overwriting an existing key. Empty keys are
// ignored.
func (r *Report) Add(key, value string) *Report {
	if r == nil || key == "" {
		return r
	}
	key = truncate(key, MaxEntryKeyLen)
	if _, exists := r.pb.Entries[key]; !exists && len(r.pb.Entries) >= MaxEntryCount {
		return r
	}
	r.pb.Entries[key] = truncate(value, MaxEntryValueLen)
	return r
}

// AddInt is a convenience for numeric entries.
func (r *Report) AddInt(key string, value int) *Report {
	return r.Add(key, intToString(value))
}

// AddBool is a convenience for boolean entries.
func (r *Report) AddBool(key string, value bool) *Report {
	if value {
		return r.Add(key, "true")
	}
	return r.Add(key, "false")
}

// Send hands the report off to the buffered sink. Returns immediately. If the
// sink is full, the report is silently discarded. Calls on a nil Report are no-ops.
func (r *Report) Send() {
	if r == nil {
		return
	}
	p := producerFromContext(r.ctx)
	if p == nil {
		return
	}
	p.sink.Enqueue(r.pb)
}

// Quick is the shorthand for New(ctx, topic).Add(k,v)...Send() when there is
// no point in holding the report between construction and send. The entries
// are supplied as alternating key/value pairs; a trailing odd argument is
// ignored.
func Quick(ctx context.Context, topic string, kv ...string) {
	r := New(ctx, topic)
	for i := 0; i+1 < len(kv); i += 2 {
		r.Add(kv[i], kv[i+1])
	}
	r.Send()
}

func intToString(v int) string {
	// Avoid pulling strconv into the hot path of disabled callers; cheap enough.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
