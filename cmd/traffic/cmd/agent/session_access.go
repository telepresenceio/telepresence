package agent

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
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

	// sessionAccessNoneTTL is how long a none answer is reused.
	sessionAccessNoneTTL = 15 * time.Second

	// sessionAccessTimeout bounds one AuthorizeSessionAccess call.
	sessionAccessTimeout = 5 * time.Second

	// sessionAccessPruneAt is the cache size at which expired entries are dropped.
	sessionAccessPruneAt = 256
)

// accessLevel is ordered; a higher level includes the lower ones.
type accessLevel int

const (
	accessNone accessLevel = iota
	accessNetwork
	accessFiles
)

// accessLevelFrom maps a level received from the traffic-manager. Anything that
// this agent does not know means no access.
func accessLevelFrom(l rpc.AuthorizeSessionAccessResponse_Level) accessLevel {
	switch l {
	case rpc.AuthorizeSessionAccessResponse_NETWORK:
		return accessNetwork
	case rpc.AuthorizeSessionAccessResponse_FILES:
		return accessFiles
	default:
		return accessNone
	}
}

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

	// unimplemented is set once the traffic-manager has answered that it doesn't
	// implement AuthorizeSessionAccess.
	unimplemented atomic.Bool

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
// accessNone and is not cached. When the traffic-manager doesn't implement the
// call, the session credential check alone decides, so every session gets
// accessFiles.
func (a *sessionAuthorizer) level(ctx context.Context, clientSession tunnel.SessionID) accessLevel {
	if a.unimplemented.Load() {
		return accessFiles
	}
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
		if status.Code(err) == codes.Unimplemented {
			if a.unimplemented.CompareAndSwap(false, true) {
				clog.Infof(ctx, "the traffic-manager doesn't implement AuthorizeSessionAccess; session access is decided by the session credential alone")
			}
			return accessFiles
		}
		clog.Warnf(ctx, "unable to authorize access for session %s: %v", clientSession, err)
		return accessNone
	}
	level := accessLevelFrom(resp.GetLevel())

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
	if level == accessNone {
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

// needsFileAccess reports whether a dial to dst requires accessFiles: loopback and
// unspecified addresses on any port, and the pod's own addresses (podIPs or a
// local interface address) on the sftp or ftp port. Everything else needs only
// accessNetwork. When the interface addresses cannot be read, an address is
// treated as the pod's own.
func needsFileAccess(dst netip.AddrPort, sftpPort, ftpPort uint16, podIPs ...netip.Addr) bool {
	ip := dst.Addr().Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	port := dst.Port()
	if port == 0 || (port != sftpPort && port != ftpPort) {
		return false
	}
	for _, pip := range podIPs {
		if pip.IsValid() && ip == pip.Unmap() {
			return true
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
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
