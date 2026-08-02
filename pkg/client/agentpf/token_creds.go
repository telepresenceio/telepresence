package agentpf

import (
	"context"

	"google.golang.org/grpc/credentials"

	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
)

// tokenCredentials implements credentials.PerRPCCredentials, attaching the session
// token provider returns, if any, as gRPC metadata on every call. Both the QUIC and
// port-forward dial paths for one agent share the same *grpc.ClientConn (see dialAgent),
// so wiring this in once as a DialOption there covers both transports. See "Item 5 hook"
// in docs/plans/auth-hardening/file-sharing-auth.md.
type tokenCredentials struct {
	provider func(ctx context.Context) string
}

var _ credentials.PerRPCCredentials = tokenCredentials{}

// GetRequestMetadata implements credentials.PerRPCCredentials. It returns no metadata
// at all -- rather than an entry with an empty value -- when provider is nil or yields
// "", which is the case whenever the connector session has no session credential yet
// (an older manager, or the fetch failed); the agent then sees exactly what it saw
// before this credential existed.
func (c tokenCredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	if c.provider == nil {
		return nil, nil
	}
	token := c.provider(ctx)
	if token == "" {
		return nil, nil
	}
	return map[string]string{sessiontoken.MetadataKey: token}, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials. The token rides in
// cleartext gRPC metadata on the plaintext, port-forwarded transport by design (see the
// package doc reference above), so this is never required.
func (c tokenCredentials) RequireTransportSecurity() bool {
	return false
}
