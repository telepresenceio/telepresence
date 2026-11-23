package agent

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const (
	// sessionAccessTTL is how long an answer from the traffic-manager is reused.
	sessionAccessTTL = 5 * time.Minute

	// sessionAccessNoneTTL is how long a LEVEL_NONE answer is reused.
	sessionAccessNoneTTL = 15 * time.Second

	// sessionAccessTimeout bounds one AuthorizeSessionAccess call.
	sessionAccessTimeout = 5 * time.Second

	// sessionAccessPruneAt is the cache size at which expired entries are dropped.
	sessionAccessPruneAt = 256
)

type accessLevel = rpc.AuthorizeSessionAccessResponse_Level

type accessEntry struct {
	level   accessLevel
	expires time.Time
}

// sessionAuthorizer asks the traffic-manager what a client session may do
// through this agent, and caches the answer per client session.
type sessionAuthorizer struct {
	manager rpc.ManagerClient
	session *rpc.SessionInfo
	now     func() time.Time

	mu    sync.Mutex
	cache map[tunnel.SessionID]accessEntry
}

func newSessionAuthorizer(manager rpc.ManagerClient, session *rpc.SessionInfo) *sessionAuthorizer {
	return &sessionAuthorizer{
		manager: manager,
		session: session,
		now:     time.Now,
		cache:   make(map[tunnel.SessionID]accessEntry),
	}
}

// level returns the access level of the client session. A failed call counts as
// rpc.AuthorizeSessionAccessResponse_LEVEL_NONE and is not cached.
func (a *sessionAuthorizer) level(ctx context.Context, clientSession tunnel.SessionID) accessLevel {
	now := a.now()
	a.mu.Lock()
	e, ok := a.cache[clientSession]
	a.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.level
	}

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionAccessTimeout)
	defer cancel()
	resp, err := a.manager.AuthorizeSessionAccess(callCtx, &rpc.AuthorizeSessionAccessRequest{
		Session:         a.session,
		ClientSessionId: string(clientSession),
	})
	if err != nil {
		clog.Warnf(ctx, "unable to authorize access for session %s: %v", clientSession, err)
		return rpc.AuthorizeSessionAccessResponse_LEVEL_NONE
	}
	level := resp.GetLevel()

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= sessionAccessPruneAt {
		for id, e := range a.cache {
			if !now.Before(e.expires) {
				delete(a.cache, id)
			}
		}
	}
	ttl := sessionAccessTTL
	if level == rpc.AuthorizeSessionAccessResponse_LEVEL_NONE {
		ttl = sessionAccessNoneTTL
	}
	a.cache[clientSession] = accessEntry{level: level, expires: now.Add(ttl)}
	return level
}

// requireAccess returns PermissionDenied when the client session has a lower access
// level than need. It never rejects unless the traffic-manager enforces authentication.
func (a *fileShareAuth) requireAccess(ctx context.Context, clientSession tunnel.SessionID, need accessLevel) error {
	if !a.enforcing() {
		return nil
	}
	sa := a.authorizer.Load()
	if sa == nil || sa.level(ctx, clientSession) < need {
		return status.Errorf(codes.PermissionDenied, "session %q is not authorized to use this agent", clientSession)
	}
	return nil
}

// isOwnAddress reports whether dialing ip reaches this pod itself: loopback, the
// unspecified address, one of the pod's addresses or an address of a local interface.
func isOwnAddress(ip netip.Addr, podIPs ...netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	for _, pip := range podIPs {
		if pip.IsValid() && ip == pip.Unmap() {
			return true
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		// Unable to tell, so treat the address as the pod's own.
		return true
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if a, ok := netip.AddrFromSlice(ipNet.IP); ok && a.Unmap() == ip {
				return true
			}
		}
	}
	return false
}
