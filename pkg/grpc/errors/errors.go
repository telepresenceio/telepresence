package errors

import (
	"context"
	"errors"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
)

// Error is like status.Error, but it will never return nil. Instead, it panics if the code is codes.Ok
//
// The sole purpose of this function is to remedy https://youtrack.jetbrains.com/issue/GO-19802
func Error(c codes.Code, msg string) error {
	err := status.Error(c, msg)
	if err == nil {
		panic("foo")
	}
	return err
}

// Errorf is like status.Errorf, but it will never return nil. Instead, it panics if the code is codes.Ok
//
// The sole purpose of this function is to remedy https://youtrack.jetbrains.com/issue/GO-19802
func Errorf(c codes.Code, format string, a ...any) error {
	err := status.Errorf(c, format, a...)
	if err == nil {
		panic("foo")
	}
	return err
}

func FromError(err error, defaultCode codes.Code, defaultMessage string) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	switch {
	case ok:
		err = st.Err()
	case errors.Is(err, context.Canceled):
		err = status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		err = status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, os.ErrNotExist), k8sErrors.IsNotFound(err):
		err = status.Error(codes.NotFound, err.Error())
	default:
		err = status.Error(defaultCode, defaultMessage)
	}
	return err
}
