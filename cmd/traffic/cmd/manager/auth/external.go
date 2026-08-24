package auth

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Admission-control defaults for the external listener's TokenReviews -- the
// expensive, API-server-bound step a hostile caller wants to trigger
// repeatedly (see the package doc comment on ExternalInterceptor). Applied by
// WithReviewAdmission on a review, never on a cached token.
const (
	// externalMaxConcurrentAuth caps the TokenReviews in flight at once, so a
	// burst of connections cannot exhaust goroutines or flood the API server
	// with concurrent reviews.
	externalMaxConcurrentAuth = 64

	// externalAuthQPS and externalAuthBurst bound the steady-state and burst
	// rate of TokenReviews.
	externalAuthQPS   = 50
	externalAuthBurst = 100

	// externalMaxAuthMetadataLen bounds the length of the "authorization" metadata
	// value accepted on the external listener. A Kubernetes bearer token is at most a
	// few KiB; this generously allows for that while rejecting anything designed to
	// waste CPU on parsing an oversized value.
	externalMaxAuthMetadataLen = 8 * 1024
)

// ambiguousCredentialsMessage is returned when a call presents both a bearer token and
// a verified transport (mTLS) principal: the plan's single-credential rule sidesteps
// defining identity equality across the two mechanisms, where username, groups, and
// extra claims can all legitimately differ and would change the resulting SAR.
const ambiguousCredentialsMessage = "this connection presents both a bearer token and a client certificate; present exactly one"

const staleCAGenerationMessage = "client certificate was verified against a superseded client CA; reconnect required"

const expiredCertMessage = "client certificate has expired"

// ExternalInterceptor authenticates every call on the external listener: the regular
// bearer-token path (shared with the internal Interceptor), or the transport principal
// a verified client certificate produced at handshake time, re-validated on every
// call. A call presenting both credential forms is rejected, review admission bounds
// the TokenReviews an unauthenticated caller can trigger, and there is no permissive
// mode.
type ExternalInterceptor struct {
	inner   *Interceptor
	caPool  *ClientCAPool
	metrics *Metrics
}

// NewExternalInterceptor creates an ExternalInterceptor that authenticates calls using
// inner's Authenticator for the bearer-token path and caPool's current generation to
// validate a transport principal, recording outcomes to metrics. A nil metrics defaults
// to a set of unregistered counters.
func NewExternalInterceptor(inner *Interceptor, caPool *ClientCAPool, metrics *Metrics) *ExternalInterceptor {
	if metrics == nil {
		metrics = unregisteredMetrics()
	}
	return &ExternalInterceptor{
		inner:   inner,
		caPool:  caPool,
		metrics: metrics,
	}
}

// Unary returns a grpc.UnaryServerInterceptor that authenticates the call.
func (e *ExternalInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if skipAuth(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := e.authenticate(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream returns a grpc.StreamServerInterceptor that authenticates the call.
func (e *ExternalInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skipAuth(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := e.authenticate(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &authenticatedStream{ServerStream: ss, ctx: ctx})
	}
}

// authenticate applies the admission controls and credential rules described on
// ExternalInterceptor, then returns a context carrying the resulting Principal.
func (e *ExternalInterceptor) authenticate(ctx context.Context, method string) (context.Context, error) {
	_, tokenPresent := bearerTokenFrom(ctx)
	tp := TransportPrincipalFrom(ctx)

	if tokenPresent && tp != nil {
		return ctx, status.Error(codes.Unauthenticated, ambiguousCredentialsMessage)
	}

	if tokenPresent {
		if authMetadataTooLong(ctx) {
			return ctx, status.Error(codes.Unauthenticated, unauthenticatedMessage)
		}
		newCtx, err := e.inner.authenticate(ctx, method)
		if err != nil {
			return ctx, err
		}
		markAuthenticated(ctx)
		return newCtx, nil
	}

	if tp != nil {
		if err := e.validateTransportPrincipal(tp); err != nil {
			return ctx, err
		}
		markAuthenticated(ctx)
		return WithPrincipal(ctx, tp.Principal), nil
	}

	return ctx, status.Error(codes.Unauthenticated, unauthenticatedMessage)
}

// authMetadataTooLong reports whether the "authorization" metadata value exceeds
// externalMaxAuthMetadataLen, checked before any parsing so an oversized value costs
// nothing beyond a length check.
func authMetadataTooLong(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	vs := md.Get(authorizationHeader)
	return len(vs) > 0 && len(vs[0]) > externalMaxAuthMetadataLen
}

// validateTransportPrincipal rejects tp when its certificate has expired or its client
// CA generation is no longer current -- both can change after the one-time handshake
// verification.
func (e *ExternalInterceptor) validateTransportPrincipal(tp *TransportPrincipal) error {
	if time.Now().After(tp.NotAfter) {
		return status.Error(codes.Unauthenticated, expiredCertMessage)
	}
	if e.caPool != nil {
		if _, generation := e.caPool.Snapshot(); tp.CAGeneration != generation {
			return status.Error(codes.Unauthenticated, staleCAGenerationMessage)
		}
	}
	return nil
}

// TransportPrincipalFrom returns the TransportPrincipal attached to ctx's peer AuthInfo
// by the external listener's transport credentials, or nil if there is none -- no peer
// info, no client certificate presented, or the certificate didn't verify to a Principal.
func TransportPrincipalFrom(ctx context.Context) *TransportPrincipal {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	ai, ok := p.AuthInfo.(*externalAuthInfo)
	if !ok {
		return nil
	}
	return ai.TransportPrincipal
}

// markAuthenticated cancels ctx's connection's unauthenticated-idle timer, if any. Called
// on the first successful authentication of a connection; a no-op on every call after
// that, and a no-op for a connection that isn't tracked by an idleTimeoutListener (e.g.
// in tests that construct a context directly).
func markAuthenticated(ctx context.Context) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return
	}
	ai, ok := p.AuthInfo.(*externalAuthInfo)
	if !ok || ai.idle == nil {
		return
	}
	ai.idle.MarkAuthenticated()
}
