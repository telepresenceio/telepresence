package watcher

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// A code listed in permanentCodes must stop the retry loop and surface the error
// rather than spinning (the cause of the "list of an unmanaged namespace hangs" timeout).
func TestWatchWithRetry_PermanentCodeStops(t *testing.T) {
	calls := 0
	sp := func(context.Context) (grpc.ServerStreamingClient[emptypb.Empty], error) {
		calls++
		return nil, status.Error(codes.FailedPrecondition, "namespace x is not managed")
	}
	err := WatchWithRetry(context.Background(), "test", time.Millisecond,
		sp, func(*emptypb.Empty) error { return nil }, nil, codes.FailedPrecondition)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, 1, calls, "a permanent code must not be retried")
}

// Without permanentCodes the same error is treated as transient and retried.
func TestWatchWithRetry_RetriesByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	sp := func(context.Context) (grpc.ServerStreamingClient[emptypb.Empty], error) {
		calls++
		if calls >= 3 {
			cancel()
		}
		return nil, status.Error(codes.FailedPrecondition, "transient")
	}
	err := WatchWithRetry(ctx, "test", time.Millisecond,
		sp, func(*emptypb.Empty) error { return nil }, nil)
	require.NoError(t, err) // a canceled context ends the retry with a nil error
	require.GreaterOrEqual(t, calls, 3, "without a permanent code the error is retried")
}
