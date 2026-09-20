package state

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type testWorkloadWatcher struct {
	ch chan []Event
}

type initialWorkloadWatcher struct{}

func (w testWorkloadWatcher) Subscribe(context.Context, string) <-chan []Event {
	return w.ch
}

func (testWorkloadWatcher) Close() {}

func (initialWorkloadWatcher) Subscribe(ctx context.Context, _ string) <-chan []Event {
	ch := make(chan []Event, 1)
	ch <- []Event{}
	go func() { <-ctx.Done() }()
	return ch
}

func (initialWorkloadWatcher) Close() {}

type blockingWorkloadStream struct {
	ctx     context.Context
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type noopWorkloadStream struct {
	ctx context.Context
}

type errorWorkloadStream struct {
	noopWorkloadStream
	err error
}

func (s noopWorkloadStream) Send(*rpc.WorkloadEventsDelta) error { return nil }
func (s noopWorkloadStream) SetHeader(metadata.MD) error         { return nil }
func (s noopWorkloadStream) SendHeader(metadata.MD) error        { return nil }
func (s noopWorkloadStream) SetTrailer(metadata.MD)              {}
func (s noopWorkloadStream) Context() context.Context            { return s.ctx }
func (s noopWorkloadStream) SendMsg(any) error                   { return nil }
func (s noopWorkloadStream) RecvMsg(any) error                   { return nil }

func (s errorWorkloadStream) Send(*rpc.WorkloadEventsDelta) error { return s.err }

func (s *blockingWorkloadStream) Send(*rpc.WorkloadEventsDelta) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func (s *blockingWorkloadStream) SetHeader(metadata.MD) error  { return nil }
func (s *blockingWorkloadStream) SendHeader(metadata.MD) error { return nil }
func (s *blockingWorkloadStream) SetTrailer(metadata.MD)       {}
func (s *blockingWorkloadStream) Context() context.Context     { return s.ctx }
func (s *blockingWorkloadStream) SendMsg(any) error            { return nil }
func (s *blockingWorkloadStream) RecvMsg(any) error            { return nil }

func TestWorkloadInfoWatcherDoesNotDetachBlockedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.NewContext(t, false))
	defer cancel()
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())

	workloads := testWorkloadWatcher{ch: make(chan []Event, 1)}
	s := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
	}
	s.workloadWatchers.Store("", workloads)
	sessionID := tunnel.SessionID("client")
	s.addClient(sessionID, &rpc.ClientInfo{Namespace: "default"}, nil, time.Now())

	deployment := &apps.Deployment{ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default"}}
	wl, ok := workload.FromAny(deployment)
	require.True(t, ok)
	workloads.ch <- []Event{{Type: EventTypeAdd, Workload: wl}}

	stream := &blockingWorkloadStream{
		ctx:     ctx,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() {
		result <- s.NewWorkloadInfoWatcher(sessionID, "default").Watch(ctx, stream)
	}()

	select {
	case <-stream.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for workload stream send")
	}

	// The stream send must remain owned by Watch. A timer callback owning this
	// blocked send lets Watch return immediately here and leaves the callback
	// retaining the stream and its pending snapshot.
	cancel()
	select {
	case err := <-result:
		t.Fatalf("Watch returned while a detached send was still blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(stream.release)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Watch did not return after blocked send was released")
	}
}

func TestWorkloadInfoWatcherReturnsStreamSendError(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.NewContext(t, false))
	defer cancel()
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())

	workloads := testWorkloadWatcher{ch: make(chan []Event, 1)}
	s := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
	}
	s.workloadWatchers.Store("", workloads)
	sessionID := tunnel.SessionID("client")
	s.addClient(sessionID, &rpc.ClientInfo{Namespace: "default"}, nil, time.Now())
	workloads.ch <- []Event{} // An empty initial snapshot must still be sent.

	wantErr := errors.New("stream closed")
	result := make(chan error, 1)
	go func() {
		stream := errorWorkloadStream{noopWorkloadStream: noopWorkloadStream{ctx: ctx}, err: wantErr}
		result <- s.NewWorkloadInfoWatcher(sessionID, "default").Watch(ctx, stream)
	}()

	select {
	case err := <-result:
		require.ErrorIs(t, err, wantErr)
	case <-time.After(time.Second):
		t.Fatal("Watch did not return stream.Send error")
	}
}

func TestWorkloadInfoWatcherBlockedSendChurnCleansUp(t *testing.T) {
	ctx := mutator.WithMap(testutil.NewContext(t, false), mutator.NewWatcher())
	s := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
	}
	s.workloadWatchers.Store("", initialWorkloadWatcher{})
	sessionID := tunnel.SessionID("client")
	s.addClient(sessionID, &rpc.ClientInfo{Namespace: "default"}, nil, time.Now())

	baseline := runtime.NumGoroutine()
	peak := baseline
	for range 100 {
		watchCtx, cancel := context.WithCancel(ctx)
		stream := &blockingWorkloadStream{
			ctx:     watchCtx,
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		result := make(chan error, 1)
		go func() {
			result <- s.NewWorkloadInfoWatcher(sessionID, "default").Watch(watchCtx, stream)
		}()
		select {
		case <-stream.started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for blocked send")
		}
		if current := runtime.NumGoroutine(); current > peak {
			peak = current
		}
		cancel()
		close(stream.release)
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("Watch did not return after blocked send was released")
		}
	}

	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+8
	}, 2*time.Second, 10*time.Millisecond)
	t.Logf("goroutines baseline=%d peak=%d final=%d", baseline, peak, runtime.NumGoroutine())
}
