package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/telepresenceio/clog"
)

const (
	versionMethod       = "/telepresence.manager.Manager/Version"
	healthMethodPrefix  = "/grpc.health.v1.Health/"
	authorizationHeader = "authorization"
	bearerPrefix        = "bearer "
	bearerPrefixLen     = len(bearerPrefix)
)

// Interceptor authenticates bearer tokens on incoming gRPC calls. It is permissive: a missing or
// rejected token lets the call proceed without a Principal.
type Interceptor struct {
	auth *Authenticator
}

// NewInterceptor creates an Interceptor that authenticates calls using a.
func NewInterceptor(a *Authenticator) *Interceptor {
	return &Interceptor{auth: a}
}

// Unary returns a grpc.UnaryServerInterceptor that authenticates the call.
func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skipAuth(info.FullMethod) {
			return handler(ctx, req)
		}
		return handler(i.authenticate(ctx, info.FullMethod), req)
	}
}

// Stream returns a grpc.StreamServerInterceptor that authenticates the call.
func (i *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skipAuth(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx := i.authenticate(ss.Context(), info.FullMethod)
		return handler(srv, &authenticatedStream{ServerStream: ss, ctx: ctx})
	}
}

func skipAuth(method string) bool {
	return method == versionMethod || strings.HasPrefix(method, healthMethodPrefix)
}

// authenticate reads the bearer token from ctx's incoming metadata and returns a context carrying
// the resulting Principal. Failures are logged, never fatal to the call.
func (i *Interceptor) authenticate(ctx context.Context, method string) context.Context {
	token, present := bearerTokenFrom(ctx)
	if token == "" {
		if present {
			clog.Debugf(ctx, "ignoring malformed authorization metadata for %s", method)
		} else {
			clog.Tracef(ctx, "unauthenticated call to %s", method)
		}
		return ctx
	}
	p, err := i.auth.Authenticate(ctx, token)
	switch {
	case err == nil:
		return WithPrincipal(ctx, p)
	case errors.Is(err, ErrInvalidToken):
		clog.Warnf(ctx, "call to %s presented an invalid bearer token", method)
	default:
		clog.Errorf(ctx, "token authentication unavailable for %s: %v", method, err)
	}
	return ctx
}

// bearerTokenFrom extracts the bearer token from the "authorization" metadata key. present is true
// when the key was set, even if the value didn't parse as a bearer token.
func bearerTokenFrom(ctx context.Context) (token string, present bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vs := md.Get(authorizationHeader)
	if len(vs) == 0 {
		return "", false
	}
	v := vs[0]
	if len(v) < bearerPrefixLen || !strings.EqualFold(v[:bearerPrefixLen], bearerPrefix) {
		return "", true
	}
	return strings.TrimSpace(v[bearerPrefixLen:]), true
}

type authenticatedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedStream) Context() context.Context {
	return s.ctx
}
