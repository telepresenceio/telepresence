package watcher

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WatchWithRetry establishes a server-streaming RPC connection and handles streamed data using the provided handler, with support for automatic retries on failure.
//
// The error returned will be nil when the retry operation ends because the context is canceled.
//
// Errors returned by the handler will be treated as permanent and returned as-is.
//
// Errors returned by the stream provider and the repair will be treated as transient and retried. The backup.Permanent wrapper can be used to override this behavior.
//
// A gRPC status code listed in permanentCodes is treated as permanent (not retried) for errors from both the stream
// provider and Recv. Use this for codes that retrying cannot resolve, such as FailedPrecondition.
func WatchWithRetry[T any](
	ctx context.Context,
	name string,
	retryInterval time.Duration,
	streamProvider func(context.Context) (grpc.ServerStreamingClient[T], error),
	handler func(*T) error,
	repair func() error,
	permanentCodes ...codes.Code,
) error {
	retryCount := 0
	err := backoff.Retry(func() error {
		if retryCount > 0 && repair != nil {
			if err := repair(); err != nil {
				return err
			}
		}
		retryCount++
		stream, err := streamProvider(ctx)
		switch c := status.Code(err); c {
		case codes.OK:
		case codes.Unimplemented:
			return backoff.Permanent(err)
		default:
			if slices.Contains(permanentCodes, c) {
				return backoff.Permanent(err)
			}
			return fmt.Errorf("error when calling stream provider for %s: %w", name, err)
		}
		defer func() {
			_ = stream.CloseSend()
		}()
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				value, err := stream.Recv()
				switch c := status.Code(err); c {
				case codes.OK:
					err = handler(value)
					if err != nil {
						return backoff.Permanent(err)
					}
				case codes.Unimplemented:
					return backoff.Permanent(err)
				default:
					if slices.Contains(permanentCodes, c) {
						return backoff.Permanent(err)
					}
					return fmt.Errorf("error when calling Recv for %s: %w", name, err)
				}
			}
		}
	}, backoff.WithContext(backoff.NewConstantBackOff(retryInterval), ctx))
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return err
}
