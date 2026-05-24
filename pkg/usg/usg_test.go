package usg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// withTestProducer returns a context with a producer attached, mirroring what
// InstallClient does on a configured client. The package's higher-level
// InstallClient and InstallManager helpers rely on config loaded via the
// client package, so for unit tests we go straight to newProducer.
func withTestProducer(t *testing.T) (context.Context, Sink) {
	t.Helper()
	sink := NewMemSink()
	p := newProducer(SourceClient, "test-install", "v0.0.0-test", sink)
	return WithProducer(context.Background(), p), sink
}

func TestNewAddSend_Happy(t *testing.T) {
	ctx, sink := withTestProducer(t)

	r := New(ctx, "cmd.test")
	if r == nil {
		t.Fatal("New returned nil for enabled producer")
	}
	r.Add("k1", "v1").AddInt("k2", 42).AddBool("k3", true)
	r.Send()

	got := sink.Drain(0)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d", len(got))
	}
	want := map[string]string{"k1": "v1", "k2": "42", "k3": "true"}
	for k, v := range want {
		if got[0].Entries[k] != v {
			t.Errorf("entry %q: want %q, got %q", k, v, got[0].Entries[k])
		}
	}
	if got[0].Topic != "cmd.test" || got[0].InstallationId != "test-install" {
		t.Errorf("topic/installation id wrong: %+v", got[0])
	}
	if got[0].Os == "" || got[0].Arch == "" || got[0].Version != "v0.0.0-test" {
		t.Errorf("os/arch/version not populated: %+v", got[0])
	}
}

func TestNoProducer_NoOp(t *testing.T) {
	// When no producer is installed on ctx, reporting is off: New returns
	// nil, and all nil-Report methods are safe no-ops.
	ctx := context.Background()
	r := New(ctx, "cmd.test")
	if r != nil {
		t.Fatal("expected nil report without a producer on ctx")
	}
	r.Add("k", "v").AddInt("n", 1).AddBool("b", false).Send()
}

func TestQuick(t *testing.T) {
	ctx, sink := withTestProducer(t)
	Quick(ctx, "cmd.quick", "a", "1", "b", "2")
	got := sink.Drain(0)
	if len(got) != 1 || got[0].Topic != "cmd.quick" {
		t.Fatalf("unexpected drain: %+v", got)
	}
	if got[0].Entries["a"] != "1" || got[0].Entries["b"] != "2" {
		t.Errorf("entries wrong: %+v", got[0].Entries)
	}
}

func TestQueueOverflow(t *testing.T) {
	ctx, sink := withTestProducer(t)
	const extra = 5
	for i := 0; i < MaxQueueSize+extra; i++ {
		Quick(ctx, "cmd.test", "i", intToString(i))
	}
	if n := sink.Len(); n != MaxQueueSize {
		t.Errorf("queue should cap at %d, got %d", MaxQueueSize, n)
	}
	// On overflow, the most recent reports must win. Drain and verify the
	// retained entries are the last MaxQueueSize ones (i=extra..extra+cap-1).
	got := sink.Drain(0)
	if len(got) != MaxQueueSize {
		t.Fatalf("drained %d entries, want %d", len(got), MaxQueueSize)
	}
	for j, r := range got {
		want := intToString(extra + j)
		if r.Entries["i"] != want {
			t.Errorf("entry %d: want i=%q, got i=%q", j, want, r.Entries["i"])
		}
	}
}

func TestMemSink_FIFO(t *testing.T) {
	sink := NewMemSink()
	for i := 0; i < 3; i++ {
		sink.Enqueue(&usgrpc.UsageReport{Topic: "t" + string(rune('0'+i))})
	}
	got := sink.Drain(2)
	if len(got) != 2 || got[0].Topic != "t0" || got[1].Topic != "t1" {
		t.Errorf("FIFO order wrong: %v", got)
	}
	got = sink.Drain(0)
	if len(got) != 1 || got[0].Topic != "t2" {
		t.Errorf("remaining drain wrong: %v", got)
	}
}

func TestDiskSink_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first, err := NewDiskSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Enqueue(&usgrpc.UsageReport{Topic: "persisted", InstallationId: "id"})
	if n := first.Len(); n != 1 {
		t.Fatalf("after enqueue: len=%d", n)
	}

	// Simulate process restart by constructing a fresh sink over the same dir.
	second, err := NewDiskSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := second.Drain(0)
	if len(got) != 1 || got[0].Topic != "persisted" {
		t.Fatalf("after reload: %+v", got)
	}
	if n := second.Len(); n != 0 {
		t.Errorf("post-drain len: %d, want 0", n)
	}
}

func TestEntryLimits(t *testing.T) {
	ctx, sink := withTestProducer(t)

	r := New(ctx, "cmd.test")
	// Empty key is ignored.
	r.Add("", "ignored")
	// Oversized key is truncated.
	longKey := strings.Repeat("k", MaxEntryKeyLen+10)
	r.Add(longKey, "value")
	// Oversized value is truncated.
	longVal := strings.Repeat("v", MaxEntryValueLen+10)
	r.Add("vk", longVal)
	// Fill the entry budget; further unique keys are dropped, but overwrites
	// of existing keys still succeed.
	for i := 0; i < MaxEntryCount+5; i++ {
		r.Add("f"+intToString(i), "x")
	}
	r.Add("vk", "still-overwrites") // overwrite, must not be dropped
	r.Send()

	got := sink.Drain(0)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d", len(got))
	}
	e := got[0].Entries

	if _, ok := e[""]; ok {
		t.Errorf("empty key should have been ignored")
	}
	truncKey := strings.Repeat("k", MaxEntryKeyLen)
	if v, ok := e[truncKey]; !ok || v != "value" {
		t.Errorf("key truncation broken: got %q (present=%v)", v, ok)
	}
	if len(e["vk"]) != len("still-overwrites") {
		t.Errorf("overwrite after cap should not be dropped, got %q", e["vk"])
	}
	if len(e) > MaxEntryCount {
		t.Errorf("entry count %d exceeds MaxEntryCount %d", len(e), MaxEntryCount)
	}
}

func TestEnvelopeTruncation(t *testing.T) {
	sink := NewMemSink()
	longID := strings.Repeat("i", MaxInstallationIDLen+50)
	p := newProducer(SourceClient, longID, "v0.0.0-test", sink)
	ctx := WithProducer(context.Background(), p)

	longTopic := strings.Repeat("t", MaxTopicLen+50)
	Quick(ctx, longTopic)

	got := sink.Drain(0)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d", len(got))
	}
	if len(got[0].InstallationId) != MaxInstallationIDLen {
		t.Errorf("InstallationId len=%d, want %d", len(got[0].InstallationId), MaxInstallationIDLen)
	}
	if len(got[0].Topic) != MaxTopicLen {
		t.Errorf("Topic len=%d, want %d", len(got[0].Topic), MaxTopicLen)
	}
}

func TestFormatError(t *testing.T) {
	plain := errors.New("plain failure")
	userErr := errcat.User.New(plain)
	wrapped := fmt.Errorf("context: %w", userErr)

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"uncategorized", plain, "unknown:*errors.errorString"},
		{"categorized", userErr, "user:*errors.errorString"},
		{"wrapped categorized", wrapped, "user:*errors.errorString"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatError(c.err)
			if got != c.want {
				t.Errorf("formatError(%v): want %q, got %q", c.err, c.want, got)
			}
		})
	}
}

func TestDiskSink_OverflowDrops(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewDiskSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	const extra = 3
	for i := 0; i < MaxQueueSize+extra; i++ {
		sink.Enqueue(&usgrpc.UsageReport{Topic: "t", InstallationId: intToString(i)})
	}
	if n := sink.Len(); n != MaxQueueSize {
		t.Errorf("disk queue should cap at %d, got %d", MaxQueueSize, n)
	}
	// On overflow, the oldest files must be the ones removed. The remaining
	// entries should be the last MaxQueueSize inserts, in order.
	got := sink.Drain(0)
	if len(got) != MaxQueueSize {
		t.Fatalf("drained %d, want %d", len(got), MaxQueueSize)
	}
	for j, r := range got {
		want := intToString(extra + j)
		if r.InstallationId != want {
			t.Errorf("entry %d: want id=%q, got id=%q", j, want, r.InstallationId)
		}
	}
}
