package auth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func loggingContext(buf *bytes.Buffer) context.Context {
	h := handler.NewText(handler.Output(buf), handler.EnabledLevel(clog.LevelTrace))
	return clog.WithLogger(context.Background(), slog.New(h))
}

func newTestInterceptor(ci *fake.Clientset) *auth.Interceptor {
	return auth.NewInterceptor(auth.NewAuthenticator(ci))
}

func TestInterceptor_Unary(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(token string, audiences []string) *authnv1.TokenReviewStatus {
		if token == "good" {
			return authenticatedStatus("u", "1")
		}
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})

	i := newTestInterceptor(ci)
	unary := i.Unary()
	info := &grpc.UnaryServerInfo{FullMethod: "/telepresence.manager.Manager/Connect"}
	handler := func(ctx context.Context, _ any) (any, error) {
		return auth.PrincipalFrom(ctx), nil
	}

	t.Run("token present", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer good"))
		resp, err := unary(ctx, nil, info, handler)
		require.NoError(t, err)
		p, _ := resp.(*auth.Principal)
		require.NotNil(t, p)
		assert.Equal(t, "u", p.Username)
	})

	t.Run("no metadata", func(t *testing.T) {
		resp, err := unary(context.Background(), nil, info, handler)
		require.NoError(t, err)
		assert.Nil(t, resp)
	})

	t.Run("invalid token", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer bad"))
		resp, err := unary(ctx, nil, info, handler)
		require.NoError(t, err)
		assert.Nil(t, resp)
	})
}

func TestInterceptor_Unary_SkipsVersionAndHealth(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, nil)

	i := newTestInterceptor(ci)
	unary := i.Unary()
	handler := func(ctx context.Context, _ any) (any, error) {
		return auth.PrincipalFrom(ctx), nil
	}

	for _, method := range []string{
		"/telepresence.manager.Manager/Version",
		"/grpc.health.v1.Health/Check",
	} {
		info := &grpc.UnaryServerInfo{FullMethod: method}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer whatever"))
		_, err := unary(ctx, nil, info, handler)
		require.NoError(t, err)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context {
	return f.ctx
}

func TestInterceptor_Stream(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeTokenReviews(ci, func(token string, audiences []string) *authnv1.TokenReviewStatus {
		if token == "good" {
			return authenticatedStatus("u", "1")
		}
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})

	i := newTestInterceptor(ci)
	stream := i.Stream()
	info := &grpc.StreamServerInfo{FullMethod: "/telepresence.manager.Manager/WatchAgentPods"}

	var seen *auth.Principal
	handler := func(_ any, ss grpc.ServerStream) error {
		seen = auth.PrincipalFrom(ss.Context())
		return nil
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer good"))
	err := stream(nil, &fakeServerStream{ctx: ctx}, info, handler)
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "u", seen.Username)
}

func TestInterceptor_LogsNeverContainToken(t *testing.T) {
	const secretInvalid = "super-secret-invalid-token-value"
	const secretUnavailable = "super-secret-unavailable-token-value"

	t.Run("invalid token", func(t *testing.T) {
		ci := fake.NewClientset()
		k8sapi.InstallFakeTokenReviews(ci, nil)

		buf := &bytes.Buffer{}
		ctx := loggingContext(buf)
		i := newTestInterceptor(ci)
		unary := i.Unary()
		info := &grpc.UnaryServerInfo{FullMethod: "/telepresence.manager.Manager/Connect"}
		handler := func(ctx context.Context, _ any) (any, error) { return nil, nil }

		md := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+secretInvalid))
		_, err := unary(md, nil, info, handler)
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), secretInvalid)
		assert.Contains(t, buf.String(), "invalid bearer token")
	})

	t.Run("infrastructure error", func(t *testing.T) {
		ci := fake.NewClientset()
		failure := errors.New("connection refused")
		ci.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, failure
		})

		buf := &bytes.Buffer{}
		ctx := loggingContext(buf)
		i := newTestInterceptor(ci)
		unary := i.Unary()
		info := &grpc.UnaryServerInfo{FullMethod: "/telepresence.manager.Manager/Connect"}
		handler := func(ctx context.Context, _ any) (any, error) { return nil, nil }

		md := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+secretUnavailable))
		_, err := unary(md, nil, info, handler)
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), secretUnavailable)
		assert.Contains(t, buf.String(), "unavailable")
	})
}
