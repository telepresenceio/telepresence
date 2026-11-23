package agent

import (
	"context"
	"crypto/sha256"
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

// accessKey identifies a cached answer: the client session and a fingerprint of
// the credential that it was presented with.
type accessKey struct {
	session    tunnel.SessionID
	credential [sha256.Size]byte
}

type accessEntry struct {
	level   accessLevel
	expires time.Time
}

// sessionAuthorizer asks the traffic-manager what a client session may do
// through this agent, and caches the answer per client session and credential.
type sessionAuthorizer struct {
	manager rpc.ManagerClient
	session *rpc.SessionInfo
	now     func() time.Time

	// unimplemented is set once the traffic-manager has answered that it doesn't
	// implement AuthorizeSessionAccess; every request is then refused.
	unimplemented atomic.Bool

	mu    sync.Mutex
	cache map[accessKey]accessEntry
}

func newSessionAuthorizer(manager rpc.ManagerClient, session *rpc.SessionInfo) *sessionAuthorizer {
	return &sessionAuthorizer{
		manager: manager,
		session: session,
		now:     time.Now,
		cache:   make(map[accessKey]accessEntry),
	}
}

// level returns the access level of the client session that presented credential.
// A failed call counts as accessNone and is not cached. A traffic-manager that
// doesn't implement the call is too old for this agent, so every session gets
// accessNone.
func (a *sessionAuthorizer) level(ctx context.Context, clientSession tunnel.SessionID, credential string) accessLevel {
	if a.unimplemented.Load() {
		return accessNone
	}
	now := a.now()
	key := accessKey{session: clientSession, credential: sha256.Sum256([]byte(credential))}
	a.mu.Lock()
	e, ok := a.cache[key]
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
				clog.Errorf(ctx, "the traffic-manager doesn't implement AuthorizeSessionAccess; "+
					"this traffic-agent requires traffic-manager 2.34.0 or later, and refuses session access until then")
			}
			return accessNone
		}
		clog.Warnf(ctx, "unable to authorize access for session %s: %v", clientSession, err)
		return accessNone
	}
	level := accessLevelFrom(resp.GetLevel())

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= sessionAccessPruneAt {
		for k, e := range a.cache {
			if !now.Before(e.expires) {
				delete(a.cache, k)
			}
		}
	}
	ttl := sessionAccessTTL
	if level == accessNone {
		ttl = sessionAccessNoneTTL
	}
	a.cache[key] = accessEntry{level: level, expires: now.Add(ttl)}
	return level
}

// requireAccess returns PermissionDenied when the client session, presenting
// credential, has a lower access level than need. It never rejects unless the
// traffic-manager enforces authentication.
func (a *fileShareAuth) requireAccess(ctx context.Context, clientSession tunnel.SessionID, credential string, need accessLevel) error {
	if !a.enforcing() {
		return nil
	}
	sa := a.authorizer.Load()
	if sa == nil || sa.level(ctx, clientSession, credential) < need {
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
	return isOwnAddr(ip, true, podIPs...)
}

// isOwnAddr reports whether ip is one of podIPs or an address of a local interface.
// When the interface addresses cannot be read, it returns onError.
func isOwnAddr(ip netip.Addr, onError bool, podIPs ...netip.Addr) bool {
	ip = ip.Unmap()
	for _, pip := range podIPs {
		if pip.IsValid() && ip == pip.Unmap() {
			return true
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return onError
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

// isOwnSftpDestination reports whether a TCP dial to dst from this process would reach
// the agent's own SFTP listener.
func isOwnSftpDestination(dst netip.AddrPort, sftpPort uint16, podIP netip.Addr) bool {
	if sftpPort == 0 || dst.Port() != sftpPort {
		return false
	}
	ip := dst.Addr().Unmap()
	return ip.IsLoopback() || ip.IsUnspecified() || isOwnAddr(ip, false, podIP)
}
