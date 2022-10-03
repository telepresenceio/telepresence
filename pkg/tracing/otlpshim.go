package tracing

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

const MaxTraceSize = 10 * 1024 * 1024 // 10 MB

// An otlpShim is a pretend client that just collects spans without exporting them.
type otlpShim struct {
	mu         sync.Mutex
	buf1       bytes.Buffer
	buf2       bytes.Buffer
	pw         *ProtoWriter
	buf2Active bool
}

func (ts *otlpShim) Start(ctx context.Context) error {
	ts.pw = NewProtoWriter(&ts.buf1)
	return nil
}

func (ts *otlpShim) Stop(ctx context.Context) error {
	return nil
}

func (ts *otlpShim) UploadTraces(ctx context.Context, protoSpans []*tracepb.ResourceSpans) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, span := range protoSpans {
		err := ts.pw.Encode(span)
		if err != nil {
			return fmt.Errorf("failed to encode span: %w", err)
		}
	}
	if ts.activeBufSize() >= (MaxTraceSize / 2) {
		ts.swapBufs()
	}
	return nil
}

func (ts *otlpShim) activeBufSize() int {
	active, _ := ts.activeInactiveBufs()
	return active.Len()
}

func (ts *otlpShim) swapBufs() {
	_, inactive := ts.activeInactiveBufs()
	if ts.buf2Active {
		inactive = &ts.buf1
	}
	inactive.Reset()
	ts.pw.SetWriter(inactive)
	ts.buf2Active = !ts.buf2Active
}

func (ts *otlpShim) activeInactiveBufs() (*bytes.Buffer, *bytes.Buffer) {
	if ts.buf2Active {
		return &ts.buf2, &ts.buf1
	}
	return &ts.buf1, &ts.buf2
}

func (ts *otlpShim) dumpTraces() []byte {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	active, inactive := ts.activeInactiveBufs()
	// inactive is older, so:
	return bytes.Join([][]byte{
		inactive.Bytes(), active.Bytes(),
	}, nil)
}
