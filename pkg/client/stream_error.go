package client

import (
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecvEOF should be returned when a component has returned EOF from a stream.
// Do not use this if, for example, the initial dial to a stream fails.
type RecvEOF struct {
	msg string
	err error
}

func (e *RecvEOF) Error() string {
	return fmt.Sprintf("%s: %v", e.msg, e.err)
}

func (e *RecvEOF) Unwrap() error {
	return e.err
}

// WrapRecvErr wraps an error from a Recv call. If the error is nil, nil is returned.
// If the error indicates that the remote end has , a RecvEOF wrapping the error will be returned.
// Otherwise, the original error will be wrapped as fmt.Errorf("%s: %w", msg, err)
func WrapRecvErr(err error, msg string) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.Unavailable || err == io.EOF {
		return &RecvEOF{msg, err}
	}
	return fmt.Errorf("%s: %w", msg, err)
}
