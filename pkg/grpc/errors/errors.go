package errors

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
