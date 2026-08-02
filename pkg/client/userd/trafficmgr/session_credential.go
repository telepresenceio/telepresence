package trafficmgr

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// sessionCredentialExpiryMargin is how long before its expiry a cached session
// credential is treated as stale and re-fetched.
const sessionCredentialExpiryMargin = time.Minute

// sessionCredentialCache lazily fetches and caches the session-scoped credential
// returned by the manager's GetSessionCredential RPC; see "The credential" in
// docs/plans/auth-hardening/file-sharing-auth.md. It is currently used as the FTP
// mount password.
type sessionCredentialCache struct {
	mu   sync.Mutex
	cred *manager.SessionCredential
	// unsupported is set once the manager answers Unimplemented, so that
	// mounts stop asking an older manager for a credential it will never have.
	unsupported bool
}

// get returns the cached credential, fetching it with fetch when there is none
// cached, the manager isn't already known to lack the RPC, or the cached one is
// within sessionCredentialExpiryMargin of its expiry. It returns nil when the
// manager doesn't implement the RPC or the fetch fails for any other reason;
// callers then proceed without a credential, exactly as they did before this
// credential existed.
func (c *sessionCredentialCache) get(
	ctx context.Context,
	fetch func(context.Context) (*manager.SessionCredential, error),
) *manager.SessionCredential {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unsupported {
		return nil
	}
	if c.cred != nil && time.Until(c.cred.Expiry.AsTime()) > sessionCredentialExpiryMargin {
		return c.cred
	}
	cred, err := fetch(ctx)
	if err != nil {
		c.cred = nil
		if status.Code(err) == codes.Unimplemented {
			c.unsupported = true
			clog.Debug(ctx, "traffic-manager does not implement GetSessionCredential; mounts proceed without a session credential")
		} else {
			clog.Debugf(ctx, "unable to fetch session credential: %v", err)
		}
		return nil
	}
	c.cred = cred
	return cred
}

// SessionCredential returns the session-scoped credential fetched from the
// traffic-manager; see "The credential" in
// docs/plans/auth-hardening/file-sharing-auth.md. It is used as the FTP mount
// password. The fetch is lazy and the result is cached: the manager is asked
// again once the cached credential is within a minute of its expiry, or on the
// first call for a session. A nil return means the manager doesn't support the
// RPC or the fetch failed; callers then proceed without a credential, exactly
// as before this credential existed.
func (s *session) SessionCredential(ctx context.Context) *manager.SessionCredential {
	return s.sessionCredential.get(ctx, func(ctx context.Context) (*manager.SessionCredential, error) {
		return s.ManagerClient().GetSessionCredential(ctx, s.SessionInfo())
	})
}
