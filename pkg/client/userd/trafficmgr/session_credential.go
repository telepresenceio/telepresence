package trafficmgr

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// SessionCredential returns the session-scoped credential fetched from the
// traffic-manager; see "The credential" in
// docs/plans/auth-hardening/file-sharing-auth.md. It is used as the FTP mount
// password. The fetch is lazy and the result is cached: the manager is asked
// again once the cached credential is within a minute of its expiry, or on the
// first call for a session. A nil return means the manager doesn't support the
// RPC or the fetch failed; callers then proceed without a credential, exactly
// as before this credential existed.
func (s *session) SessionCredential(ctx context.Context) *manager.SessionCredential {
	return s.sessionCredential.Get(ctx, func(ctx context.Context) (*manager.SessionCredential, error) {
		return s.ManagerClient().GetSessionCredential(ctx, s.SessionInfo())
	})
}
