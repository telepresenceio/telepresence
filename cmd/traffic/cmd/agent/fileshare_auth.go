package agent

import (
	"context"
	"crypto/ecdsa"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// fileShareAuth is a late-populated holder that the agent's file-sharing listeners (FTP
// and SFTP) and its gRPC surface (WatchDial, Tunnel) consult per connection or call. It
// starts out empty -- no manager has yet supplied credential material -- in which case
// every connection or call is accepted, exactly as before this credential existed.
// RefreshQuicAgentListener populates it once the manager reports a session credential.
// The holder backs the FTP password validator, the mode consulted by the SFTP source
// gate, and verifySession, which binds WatchDial/Tunnel calls to their session; see
// "Enforcement follows the manager's authentication mode" and "Item 5 hook" in
// docs/plans/auth-hardening/file-sharing-auth.md.
type fileShareAuth struct {
	v atomic.Pointer[fileShareAuthSnapshot]
}

// fileShareAuthSnapshot is the credential material fileShareAuth holds once populated.
type fileShareAuthSnapshot struct {
	pub  *ecdsa.PublicKey // verifies session tokens (sessiontoken.Verify)
	mode string           // the manager's authentication mode; "" or non-"enforcing" never rejects
}

// set installs a freshly fetched snapshot, replacing whatever was there before.
func (a *fileShareAuth) set(pub *ecdsa.PublicKey, mode string) {
	a.v.Store(&fileShareAuthSnapshot{pub: pub, mode: mode})
}

// enforcing reports whether an unauthenticated or invalid connection must be refused:
// only once a snapshot has been populated and the manager's mode is "enforcing".
func (a *fileShareAuth) enforcing() bool {
	s := a.v.Load()
	return s != nil && s.mode == "enforcing"
}

// validatePassword implements go-ftpserver's PasswordValidator. password carries a
// session token minted by the manager (see pkg/sessiontoken). A nil snapshot means this
// agent has never heard from a manager new enough to issue credentials, so there is
// nothing to verify against and every login is accepted. A failed verification is only
// fatal in enforcing mode; otherwise it is logged and the login still succeeds, at
// debug level for "anonymous" (a legacy go-fuseftp client that predates the session
// token) and at warn level for anything else (a malformed, expired, or foreign token).
func (a *fileShareAuth) validatePassword(ctx context.Context, user, password string) error {
	s := a.v.Load()
	if s == nil {
		clog.Debugf(ctx, "fileshareauth: no session credential material yet; accepting FTP login for %q", user)
		return nil
	}
	_, err := sessiontoken.Verify(s.pub, password, time.Now())
	if err == nil {
		return nil
	}
	if s.mode == "enforcing" {
		return err
	}
	if password == "anonymous" {
		clog.Debugf(ctx, "fileshareauth: legacy anonymous FTP login for %q accepted (mode %q)", user, s.mode)
	} else {
		clog.Warnf(ctx, "fileshareauth: FTP login for %q presented an invalid session token, accepted (mode %q): %v",
			user, s.mode, err)
	}
	return nil
}

// verifySession authenticates a WatchDial or Tunnel call against the session token
// carried in ctx's incoming gRPC metadata (sessiontoken.MetadataKey), which the client
// attaches as per-RPC credentials on the plaintext, port-forwarded transport (see
// agentpf's tokenCredentials). It reports whether the token verified, and if so whether
// the session it names matches declared -- the session ID the caller's request itself
// claims.
//
// A nil snapshot means this agent has never heard from a manager new enough to issue
// credentials: there is nothing to verify against, so the call is treated exactly as
// before this check existed. A token that verifies but names a session other than
// declared is rejected in every mode -- WatchDial/Tunnel each bind one channel to one
// session, so a foreign token is never ambiguous, only wrong -- and the returned error
// names neither the token nor the session it actually verified for. An absent or invalid
// token is rejected only in enforcing mode; permissive/disabled log it (debug for
// absent, warn for invalid) and let the call through unverified, exactly like
// validatePassword.
func (a *fileShareAuth) verifySession(ctx context.Context, declared tunnel.SessionID) (verified bool, err error) {
	s := a.v.Load()
	if s == nil {
		return false, nil
	}
	var token string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vs := md.Get(sessiontoken.MetadataKey); len(vs) > 0 {
			token = vs[0]
		}
	}
	if token != "" {
		sessionID, vErr := sessiontoken.Verify(s.pub, token, time.Now())
		if vErr == nil {
			if sessionID != string(declared) {
				return false, status.Errorf(codes.PermissionDenied,
					"fileshareauth: session token does not authorize session %q", declared)
			}
			return true, nil
		}
		err = vErr
	}
	if s.mode == "enforcing" {
		return false, status.Error(codes.Unauthenticated, "fileshareauth: this agent requires a session credential")
	}
	if err == nil {
		clog.Debugf(ctx, "fileshareauth: call for session %q presented no session token; accepted (mode %q)",
			declared, s.mode)
	} else {
		clog.Warnf(ctx, "fileshareauth: call for session %q presented an invalid session token, accepted (mode %q): %v",
			declared, s.mode, err)
	}
	return false, nil
}

// fromOwnPod reports whether remote is a connection delivered through the tunnel:
// every legitimate consumer reaches the file-sharing ports through the telepresence
// tunnel, whose connections the agent itself dials, so their source address is the
// agent pod's own IP (or loopback); a foreign source is a direct cross-network
// connection that bypassed the tunnel.
func fromOwnPod(remote net.Addr, podIP netip.Addr) bool {
	tcpAddr, ok := remote.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(tcpAddr.IP)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	return ip == podIP.Unmap() || ip.IsLoopback()
}
