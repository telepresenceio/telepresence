package poddaemon

import (
	"google.golang.org/grpc/metadata"
)

// shamServerStream mostly implements google.golang.org/grpc.ServerStream by panicking with the
// string "not implemented".  The exception is that the .Context() method is not implemented; you
// should actually implement that one.
type shamServerStream struct{}

func (shamServerStream) SetHeader(metadata.MD) error  { panic("not implemented") }
func (shamServerStream) SendHeader(metadata.MD) error { panic("not implemented") }
func (shamServerStream) SetTrailer(metadata.MD)       { panic("not implemented") }
func (shamServerStream) SendMsg(m interface{}) error  { panic("not implemented") }
func (shamServerStream) RecvMsg(m interface{}) error  { panic("not implemented") }
