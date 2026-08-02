package rootd

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// sessionCredentialToken returns the signed bearer token from this session's
// session-scoped credential (see pkg/sessiontoken and "The credential" in
// docs/plans/auth-hardening/file-sharing-auth.md), fetching and caching it via
// GetSessionCredential on first use. It returns "" when the manager doesn't implement
// the RPC or the fetch fails; callers then attach nothing, exactly as an old client did
// before this credential existed.
//
// This is passed to agentpf.NewClients as the token provider attached to every agent
// gRPC call as per-RPC credentials, so the agent can bind WatchDial/Tunnel to the
// calling session on the plaintext, port-forwarded transport; see "Item 5 hook" in
// docs/plans/auth-hardening/file-sharing-auth.md.
func (s *session) sessionCredentialToken(ctx context.Context) string {
	cred := s.sessionCredential.Get(ctx, func(ctx context.Context) (*manager.SessionCredential, error) {
		return s.managerClient().GetSessionCredential(ctx, s.session)
	})
	if cred == nil {
		return ""
	}
	return cred.Token
}
