// Package sessioncred caches the session-scoped credential returned by the manager's
// GetSessionCredential RPC; see "The session credential" in docs/reference/authentication.md.
// It is shared by every client-side consumer that needs the credential: the FTP mount
// password (pkg/client/userd/trafficmgr) and the per-RPC token attached to agent gRPC
// calls on the plaintext port-forward transport (pkg/client/agentpf).
package sessioncred

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// ExpiryMargin is how long before its expiry a cached session credential is treated as
// stale and re-fetched.
const ExpiryMargin = time.Minute

// Cache lazily fetches and caches the session-scoped credential returned by the
// manager's GetSessionCredential RPC.
type Cache struct {
	mu   sync.Mutex
	cred *manager.SessionCredential
	// unsupported is set once the manager answers Unimplemented, so that callers stop
	// asking an older manager for a credential it will never have.
	unsupported bool
}

// Get returns the cached credential, fetching it with fetch when there is none
// cached, the manager isn't already known to lack the RPC, or the cached one is
// within ExpiryMargin of its expiry. It returns nil when the manager doesn't
// implement the RPC or the fetch fails for any other reason; callers then proceed
// without a credential, exactly as they did before this credential existed.
func (c *Cache) Get(
	ctx context.Context,
	fetch func(context.Context) (*manager.SessionCredential, error),
) *manager.SessionCredential {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unsupported {
		return nil
	}
	if c.cred != nil && time.Until(c.cred.Expiry.AsTime()) > ExpiryMargin {
		return c.cred
	}
	cred, err := fetch(ctx)
	if err != nil {
		c.cred = nil
		if status.Code(err) == codes.Unimplemented {
			c.unsupported = true
			clog.Debug(ctx, "traffic-manager does not implement GetSessionCredential; proceeding without a session credential")
		} else {
			clog.Debugf(ctx, "unable to fetch session credential: %v", err)
		}
		return nil
	}
	c.cred = cred
	return cred
}
