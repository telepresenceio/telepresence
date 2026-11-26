package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog/testutil"
)

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeStream) Context() context.Context   { return f.ctx }
func (fakeStream) SetHeader(metadata.MD) error  { return nil }
func (fakeStream) SendHeader(metadata.MD) error { return nil }

func TestRecoveryInterceptors(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	unary, stream := recoveryInterceptors()

	uinfo := &grpc.UnaryServerInfo{FullMethod: "/x/Y"}
	for i := 0; i < 2; i++ {
		_, err := unary(ctx, nil, uinfo, func(context.Context, any) (any, error) { panic("secret-value") })
		require.Equal(t, codes.Internal, status.Code(err))
		require.NotContains(t, err.Error(), "secret-value")
	}
	v, err := unary(ctx, nil, uinfo, func(context.Context, any) (any, error) { return "ok", nil })
	require.NoError(t, err)
	require.Equal(t, "ok", v)

	sinfo := &grpc.StreamServerInfo{FullMethod: "/x/Z"}
	ss := fakeStream{ctx: ctx}
	for i := 0; i < 2; i++ {
		err = stream(nil, ss, sinfo, func(any, grpc.ServerStream) error { panic("secret-value") })
		require.Equal(t, codes.Internal, status.Code(err))
		require.NotContains(t, err.Error(), "secret-value")
	}
	require.NoError(t, stream(nil, ss, sinfo, func(any, grpc.ServerStream) error { return nil }))
}
