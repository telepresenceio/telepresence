package server

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

type connIDKey struct{}

// ConnID returns the number the server assigned to the connection that the call identified by ctx arrived
// on. The number is only available when the server was created with the TrackConns option.
func ConnID(ctx context.Context) (uint64, bool) {
	id, ok := ctx.Value(connIDKey{}).(uint64)
	return id, ok
}

// TrackConns returns a server option that assigns a unique number to every accepted connection. The number
// is available to handlers and interceptors using ConnID. When the connection closes, onClose, if non-nil,
// is called with its number.
func TrackConns(onClose func(id uint64)) grpc.ServerOption {
	return grpc.StatsHandler(&connTracker{onClose: onClose})
}

type connTracker struct {
	last    atomic.Uint64
	onClose func(id uint64)
}

func (t *connTracker) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return context.WithValue(ctx, connIDKey{}, t.last.Add(1))
}

func (t *connTracker) HandleConn(ctx context.Context, cs stats.ConnStats) {
	if _, ok := cs.(*stats.ConnEnd); ok && t.onClose != nil {
		if id, ok := ConnID(ctx); ok {
			t.onClose(id)
		}
	}
}

func (*connTracker) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (*connTracker) HandleRPC(context.Context, stats.RPCStats) {}
