package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
)

const (
	versionMethod       = "/telepresence.manager.Manager/Version"
	healthMethodPrefix  = "/grpc.health.v1.Health/"
	authorizationHeader = "authorization"
	bearerPrefix        = "bearer "
	bearerPrefixLen     = len(bearerPrefix)
)

// unauthenticatedMessage is returned to callers rejected in ModeEnforcing for
// presenting no bearer token at all.
const unauthenticatedMessage = "this traffic-manager requires an authenticated caller; the telepresence client must present a Kubernetes bearer token"

// Interceptor authenticates bearer tokens on incoming gRPC calls. Its behavior is
// governed by a Mode: ModeDisabled skips authentication entirely, ModePermissive
// authenticates but never rejects a call, and ModeEnforcing rejects calls that
// lack a valid token.
type Interceptor struct {
	auth *Authenticator
	mode Mode
}

// NewInterceptor creates an Interceptor that authenticates calls using a, behaving
// according to mode.
func NewInterceptor(a *Authenticator, mode Mode) *Interceptor {
	return &Interceptor{auth: a, mode: mode}
}

// Unary returns a grpc.UnaryServerInterceptor that authenticates the call.
func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if i.mode == ModeDisabled || skipAuth(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := i.authenticate(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream returns a grpc.StreamServerInterceptor that authenticates the call.
func (i *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if i.mode == ModeDisabled || skipAuth(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := i.authenticate(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &authenticatedStream{ServerStream: ss, ctx: ctx})
	}
}

func skipAuth(method string) bool {
	return method == versionMethod || strings.HasPrefix(method, healthMethodPrefix)
}

// authenticate reads the bearer token from ctx's incoming metadata and returns a context carrying
// the resulting Principal. In ModePermissive, failures are logged and never fatal to the call. In
// ModeEnforcing, a missing or invalid token is rejected and an infrastructure failure is reported
// as Unavailable.
func (i *Interceptor) authenticate(ctx context.Context, method string) (context.Context, error) {
	enforcing := i.mode == ModeEnforcing
	token, present := bearerTokenFrom(ctx)
	if token == "" {
		if present {
			clog.Debugf(ctx, "ignoring malformed authorization metadata for %s", method)
		} else {
			clog.Tracef(ctx, "unauthenticated call to %s", method)
		}
		if enforcing {
			return ctx, status.Error(codes.Unauthenticated, unauthenticatedMessage)
		}
		return ctx, nil
	}
	p, err := i.auth.Authenticate(ctx, token)
	switch {
	case err == nil:
		return WithPrincipal(ctx, p), nil
	case errors.Is(err, ErrInvalidToken):
		clog.Warnf(ctx, "call to %s presented an invalid bearer token", method)
		if enforcing {
			return ctx, status.Error(codes.Unauthenticated, unauthenticatedMessage)
		}
	default:
		clog.Errorf(ctx, "token authentication unavailable for %s: %v", method, err)
		if enforcing {
			return ctx, status.Errorf(codes.Unavailable, "unable to authenticate caller: %v", err)
		}
		ctx = WithAuthUnavailable(ctx)
	}
	return ctx, nil
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
