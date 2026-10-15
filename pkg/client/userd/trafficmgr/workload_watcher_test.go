package trafficmgr

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type workloadWatchManager struct {
	manager.UnimplementedManagerServer
	calls atomic.Int32
	watch func(*manager.WorkloadEventsRequest, grpc.ServerStreamingServer[manager.WorkloadEventsDelta]) error
}

func (m *workloadWatchManager) WatchWorkloads(request *manager.WorkloadEventsRequest, stream grpc.ServerStreamingServer[manager.WorkloadEventsDelta]) error {
	m.calls.Add(1)
	return m.watch(request, stream)
}

func newWorkloadWatcherTestSession(t *testing.T, m *workloadWatchManager) *session {
	t.Helper()
	s := newWorkloadSnapshotTestSession(t, nil)
	ctx, cancel := context.WithCancel(s.Context)
	cfg := client.GetDefaultConfig()
	cfg.Grpc().WatchRetryInterval = 5 * time.Millisecond
	s.Context = client.WithConfig(ctx, cfg)
	s.managerConn = dialTestManager(t, m)
	s.sessionInfo = &manager.SessionInfo{SessionId: "workload-watch-test"}
	delete(s.workloads, "app-space")
	t.Cleanup(cancel)
	return s
}

func TestWorkloadSnapshotTerminalError(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.FailedPrecondition, codes.Unimplemented} {
		t.Run(code.String(), func(t *testing.T) {
			m := &workloadWatchManager{
				watch: func(_ *manager.WorkloadEventsRequest, _ grpc.ServerStreamingServer[manager.WorkloadEventsDelta]) error {
					return status.Error(code, "cannot watch namespace")
				},
			}
			s := newWorkloadWatcherTestSession(t, m)
			result := make(chan error, 1)
			go func() {
				_, err := s.WorkloadInfoSnapshot(t.Context(), []string{"app-space"}, rpc.ListRequest_UNSPECIFIED)
				result <- err
			}()
			select {
			case err := <-result:
				require.Equal(t, code, status.Code(err))
				require.EqualValues(t, 1, m.calls.Load(), "a terminal error must not be retried")
			case <-time.After(time.Second):
				t.Fatalf("workload snapshot remains blocked after %d %s responses", m.calls.Load(), code)
			}
			s.workloadsLock.Lock()
			defer s.workloadsLock.Unlock()
			require.Empty(t, s.workloadWatchers, "a failed watch must release its pending entry")
			require.Empty(t, s.workloads, "a failed watch must not publish an empty snapshot")
		})
	}
}

type workloadWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *workloadWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestWorkloadSnapshotSharesPendingWatcher(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	m := &workloadWatchManager{}
	m.watch = func(_ *manager.WorkloadEventsRequest, stream grpc.ServerStreamingServer[manager.WorkloadEventsDelta]) error {
		if m.calls.Load() == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
		if err := stream.Send(&manager.WorkloadEventsDelta{}); err != nil {
			return err
		}
		<-stream.Context().Done()
		close(stopped)
		return nil
	}
	s := newWorkloadWatcherTestSession(t, m)

	// The early intercept watcher and snapshot calls share the session stream.
	early := make(chan error, 1)
	go func() { early <- s.ensureWatchers(s, []string{"app-space"}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("workload watch did not start")
	}
	const callers = 8
	results := make([]chan error, callers)
	var cancelFirst context.CancelFunc
	for i := range callers {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		if i == 0 {
			cancelFirst = cancel
		}
		waiting := make(chan struct{})
		waitCtx := &workloadWaitContext{Context: ctx, waiting: waiting}
		results[i] = make(chan error, 1)
		go func() {
			_, err := s.WorkloadInfoSnapshot(waitCtx, []string{"app-space"}, rpc.ListRequest_UNSPECIFIED)
			results[i] <- err
		}()
		select {
		case <-waiting:
		case <-time.After(time.Second):
			t.Fatal("snapshot did not wait for the pending watch")
		}
	}
	cancelFirst()
	select {
	case err := <-results[0]:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not release snapshot waiter")
	}
	close(release)
	for _, result := range append(results[1:], early) {
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("first snapshot did not release all waiters")
		}
	}
	require.EqualValues(t, 1, m.calls.Load(), "concurrent snapshot calls must share one watch")
	select {
	case <-stopped:
		t.Fatal("canceling one request canceled the shared watch")
	default:
	}
	// A received empty snapshot is cached, unlike a pending or denied watch.
	_, err := s.WorkloadInfoSnapshot(t.Context(), []string{"app-space"}, rpc.ListRequest_UNSPECIFIED)
	require.NoError(t, err)
	require.EqualValues(t, 1, m.calls.Load())
}

func TestWorkloadSnapshotRetriesTransientFailure(t *testing.T) {
	m := &workloadWatchManager{}
	m.watch = func(_ *manager.WorkloadEventsRequest, stream grpc.ServerStreamingServer[manager.WorkloadEventsDelta]) error {
		if m.calls.Load() == 1 {
			return status.Error(codes.Unavailable, "temporarily unavailable")
		}
		if err := stream.Send(&manager.WorkloadEventsDelta{}); err != nil {
			return err
		}
		<-stream.Context().Done()
		return nil
	}
	s := newWorkloadWatcherTestSession(t, m)
	result := make(chan error, 1)
	go func() {
		_, err := s.WorkloadInfoSnapshot(t.Context(), []string{"app-space"}, rpc.ListRequest_UNSPECIFIED)
		result <- err
	}()
	select {
	case err := <-result:
		require.NoError(t, err)
		require.EqualValues(t, 2, m.calls.Load())
	case <-time.After(time.Second):
		t.Fatal("snapshot did not recover from transient failure")
	}
}
