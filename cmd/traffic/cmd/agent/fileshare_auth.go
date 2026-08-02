package agent

import (
	"context"
	"crypto/ecdsa"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
)

// fileShareAuth is a late-populated holder that the agent's file-sharing listeners (FTP
// and SFTP) consult per connection. It starts out empty -- no manager has yet supplied
// credential material -- in which case every connection is accepted, exactly as before
// this credential existed. RefreshQuicAgentListener populates it once the manager
// reports a session credential. The holder backs the FTP password validator and the mode
// consulted by the SFTP source gate; see "Enforcement follows the manager's
// authentication mode" in docs/plans/auth-hardening/file-sharing-auth.md.
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
