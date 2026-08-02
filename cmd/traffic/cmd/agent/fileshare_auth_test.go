package agent

// This file lives in package agent (not agent_test, unlike most of this directory's
// tests) because fileShareAuth, its snapshot, and serveSftpConn are all unexported: the
// behavior under test -- what a connection sees, and what ends up in the snapshot -- has
// no exported surface to observe it through from outside the package.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog/testutil"
	agentrpc "github.com/telepresenceio/telepresence/rpc/v2/agent"
	mgrrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/sftpserver"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// generateTestKey returns a fresh ECDSA P-256 key, independent of any quictunnel.CA, so
// tests can mint tokens with arbitrary (including expired) expiries -- something
// quictunnel.CA.MintSessionToken, which always mints for its own fixed validity window,
// does not allow.
func generateTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

// TestFileShareAuth_ValidatePassword_Enforcing covers validatePassword's failure modes
// in enforcing mode: only a token that verifies against the snapshot's public key and
// hasn't expired is accepted; everything else -- including the legacy anonymous/anonymous
// login -- is rejected.
func TestFileShareAuth_ValidatePassword_Enforcing(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)
	otherKey := generateTestKey(t)

	valid, err := sessiontoken.Mint(key, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	expired, err := sessiontoken.Mint(key, "session-1", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	foreign, err := sessiontoken.Mint(otherKey, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	a := &fileShareAuth{}
	a.set(&key.PublicKey, "enforcing")
	require.True(t, a.enforcing())

	require.NoError(t, a.validatePassword(ctx, "u", valid))

	tests := []struct {
		name     string
		password string
	}{
		{"expired", expired},
		{"foreign key", foreign},
		{"garbage", "not-a-token"},
		{"legacy anonymous", "anonymous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, a.validatePassword(ctx, "u", tt.password))
		})
	}
}

// TestFileShareAuth_ValidatePassword_Permissive covers the same failure modes as
// TestFileShareAuth_ValidatePassword_Enforcing, but in permissive mode: every one of
// them is logged and still accepted, matching this agent's pre-credential behavior for
// clients that present no valid token.
func TestFileShareAuth_ValidatePassword_Permissive(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)
	otherKey := generateTestKey(t)

	valid, err := sessiontoken.Mint(key, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	expired, err := sessiontoken.Mint(key, "session-1", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	foreign, err := sessiontoken.Mint(otherKey, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	a := &fileShareAuth{}
	a.set(&key.PublicKey, "permissive")
	require.False(t, a.enforcing())

	tests := []struct {
		name     string
		password string
	}{
		{"valid", valid},
		{"expired", expired},
		{"foreign key", foreign},
		{"garbage", "not-a-token"},
		{"legacy anonymous", "anonymous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, a.validatePassword(ctx, "u", tt.password))
		})
	}
}

// TestFileShareAuth_ValidatePassword_NilSnapshotAcceptsAnything covers the state before
// any manager has ever supplied credential material: there is nothing to verify
// against, so every login succeeds, exactly as it did before this credential existed.
func TestFileShareAuth_ValidatePassword_NilSnapshotAcceptsAnything(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	a := &fileShareAuth{}
	require.False(t, a.enforcing())
	require.NoError(t, a.validatePassword(ctx, "u", "not-a-token"))
	require.NoError(t, a.validatePassword(ctx, "u", "anonymous"))
}

// TestFileShareAuth_VerifySession_ValidMatch covers verifySession's success path: a
// token that verifies and names the same session as declared is accepted regardless of
// mode.
func TestFileShareAuth_VerifySession_ValidMatch(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)
	tok, err := sessiontoken.Mint(key, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	for _, mode := range []string{"permissive", "enforcing"} {
		t.Run(mode, func(t *testing.T) {
			a := &fileShareAuth{}
			a.set(&key.PublicKey, mode)
			mdCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, tok))
			verified, err := a.verifySession(mdCtx, tunnel.SessionID("session-1"))
			require.NoError(t, err)
			require.True(t, verified)
		})
	}
}

// TestFileShareAuth_VerifySession_ValidMismatch covers verifySession's rejection of a
// token that verifies but names a session other than the one the call declares: this is
// rejected in every mode, since WatchDial/Tunnel each bind one channel to one session.
func TestFileShareAuth_VerifySession_ValidMismatch(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)
	tok, err := sessiontoken.Mint(key, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	for _, mode := range []string{"permissive", "enforcing"} {
		t.Run(mode, func(t *testing.T) {
			a := &fileShareAuth{}
			a.set(&key.PublicKey, mode)
			mdCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, tok))
			verified, err := a.verifySession(mdCtx, tunnel.SessionID("session-2"))
			require.False(t, verified)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}

// TestFileShareAuth_VerifySession_Absent covers a call with no session token at all:
// rejected only in enforcing mode; permissive lets it through unverified.
func TestFileShareAuth_VerifySession_Absent(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)

	permissive := &fileShareAuth{}
	permissive.set(&key.PublicKey, "permissive")
	verified, err := permissive.verifySession(ctx, tunnel.SessionID("session-1"))
	require.NoError(t, err)
	require.False(t, verified)

	enforcing := &fileShareAuth{}
	enforcing.set(&key.PublicKey, "enforcing")
	verified, err = enforcing.verifySession(ctx, tunnel.SessionID("session-1"))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, verified)
}

// TestFileShareAuth_VerifySession_Invalid covers a call presenting a malformed token:
// rejected only in enforcing mode, exactly like the absent case, but logged at warn
// instead of debug (not independently observable here; only the return values are
// asserted).
func TestFileShareAuth_VerifySession_Invalid(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	key := generateTestKey(t)
	mdCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, "not-a-token"))

	permissive := &fileShareAuth{}
	permissive.set(&key.PublicKey, "permissive")
	verified, err := permissive.verifySession(mdCtx, tunnel.SessionID("session-1"))
	require.NoError(t, err)
	require.False(t, verified)

	enforcing := &fileShareAuth{}
	enforcing.set(&key.PublicKey, "enforcing")
	verified, err = enforcing.verifySession(mdCtx, tunnel.SessionID("session-1"))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, verified)
}

// TestFileShareAuth_VerifySession_NilSnapshot covers the state before any manager has
// ever supplied credential material: there is nothing to verify against, so the call is
// treated exactly as before this check existed.
func TestFileShareAuth_VerifySession_NilSnapshot(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	a := &fileShareAuth{}
	verified, err := a.verifySession(ctx, tunnel.SessionID("session-1"))
	require.NoError(t, err)
	require.False(t, verified)
}

// TestFromOwnPod covers fromOwnPod's classification of a connection's remote address:
// the pod's own address (in both its plain and 4-in-6-mapped forms) and loopback are
// "from own pod"; anything else, including a non-TCP net.Addr, is foreign.
func TestFromOwnPod(t *testing.T) {
	podIPv4 := netip.MustParseAddr("10.42.0.7")
	podIPv6 := netip.MustParseAddr("2001:db8::7")

	tests := []struct {
		name   string
		remote net.Addr
		podIP  netip.Addr
		want   bool
	}{
		{
			name:   "own pod IPv4",
			remote: &net.TCPAddr{IP: net.ParseIP("10.42.0.7").To4(), Port: 1234},
			podIP:  podIPv4,
			want:   true,
		},
		{
			name:   "own pod IPv4, 4-in-6 mapped form",
			remote: &net.TCPAddr{IP: net.ParseIP("10.42.0.7"), Port: 1234}, // net.ParseIP returns the 16-byte mapped form
			podIP:  podIPv4,
			want:   true,
		},
		{
			name:   "own pod IPv6",
			remote: &net.TCPAddr{IP: net.ParseIP("2001:db8::7"), Port: 1234},
			podIP:  podIPv6,
			want:   true,
		},
		{
			name:   "loopback v4",
			remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
			podIP:  podIPv4,
			want:   true,
		},
		{
			name:   "loopback v6",
			remote: &net.TCPAddr{IP: net.ParseIP("::1"), Port: 1234},
			podIP:  podIPv4,
			want:   true,
		},
		{
			name:   "foreign IP",
			remote: &net.TCPAddr{IP: net.ParseIP("10.42.9.9"), Port: 1234},
			podIP:  podIPv4,
			want:   false,
		},
		{
			name:   "non-TCP addr",
			remote: &net.UnixAddr{Name: "/tmp/fileshare-auth-test.sock", Net: "unix"},
			podIP:  podIPv4,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fromOwnPod(tt.remote, tt.podIP))
		})
	}
}

// sftpGateFixture is the tree and confined server shared by the serveSftpConn source-gate
// tests below. It can be shared read-only across subtests; only the fileShareAuth
// snapshot, pod IP, and simulated remote address differ per test.
type sftpGateFixture struct {
	srv *sftpserver.Server
}

func newSftpGateFixture(t *testing.T) *sftpGateFixture {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644))
	srv, err := sftpserver.New(dir, t.TempDir())
	require.NoError(t, err)
	return &sftpGateFixture{srv: srv}
}

// remoteAddrConn wraps a net.Conn and reports remote from RemoteAddr instead of the
// wrapped conn's own address, so a test dialing over real loopback TCP can simulate an
// arbitrary source address at serveSftpConn's gate.
type remoteAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c *remoteAddrConn) RemoteAddr() net.Addr { return c.remote }

// listen starts a real TCP listener whose accept loop wraps every connection to report
// remote as its RemoteAddr and runs it through serveSftpConn -- exactly as agent.go's
// sftpServer wires the production listener -- and returns its address, so a test can
// dial it as a real client would.
func (f *sftpGateFixture) listen(t *testing.T, ctx context.Context, auth *fileShareAuth, podIP netip.Addr, remote net.Addr) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSftpConn(ctx, &remoteAddrConn{Conn: conn, remote: remote}, f.srv, auth, podIP)
		}
	}()
	return ln.Addr().String()
}

// requireListsFixture dials addr as an SFTP client and asserts that it can list the
// fixture's hello.txt.
func requireListsFixture(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()

	client, err := sftp.NewClientPipe(conn, conn)
	require.NoError(t, err)
	defer client.Close()

	entries, err := client.ReadDir("/")
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	require.Contains(t, names, "hello.txt")
}

// TestServeSftpConn_ForeignEnforcingCloses asserts that a connection whose source
// address is neither the pod's own nor loopback is closed before it ever receives an
// SFTP response once the snapshot's mode is enforcing.
func TestServeSftpConn_ForeignEnforcingCloses(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	f := newSftpGateFixture(t)
	auth := &fileShareAuth{}
	auth.set(nil, "enforcing")
	podIP := netip.MustParseAddr("10.42.0.7")
	foreign := &net.TCPAddr{IP: net.ParseIP("10.42.9.9"), Port: 4444}
	addr := f.listen(t, ctx, auth, podIP, foreign)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()

	_, err = sftp.NewClientPipe(conn, conn)
	require.Error(t, err, "expected the connection to be closed before completing the SFTP version exchange")
}

// TestServeSftpConn_ForeignPermissiveServes asserts that a foreign-source connection is
// still served, exactly as before this gate existed, when the snapshot's mode is
// permissive.
func TestServeSftpConn_ForeignPermissiveServes(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	f := newSftpGateFixture(t)
	auth := &fileShareAuth{}
	auth.set(nil, "permissive")
	podIP := netip.MustParseAddr("10.42.0.7")
	foreign := &net.TCPAddr{IP: net.ParseIP("10.42.9.9"), Port: 4444}
	addr := f.listen(t, ctx, auth, podIP, foreign)

	requireListsFixture(t, addr)
}

// TestServeSftpConn_OwnSourceEnforcingServes asserts that connections whose source
// address is the pod's own, or loopback, are served even in enforcing mode -- these are
// exactly the connections the tunnel delivers, since the agent dials them itself.
func TestServeSftpConn_OwnSourceEnforcingServes(t *testing.T) {
	podIP := netip.MustParseAddr("10.42.0.7")
	tests := []struct {
		name   string
		remote net.Addr
	}{
		{"own pod address", &net.TCPAddr{IP: net.ParseIP("10.42.0.7"), Port: 5555}},
		{"loopback", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t, false)
			f := newSftpGateFixture(t)
			auth := &fileShareAuth{}
			auth.set(nil, "enforcing")
			addr := f.listen(t, ctx, auth, podIP, tt.remote)

			requireListsFixture(t, addr)
		})
	}
}

// fakeQuicAgentCertManager is a minimal stand-in for the traffic-manager's
// GetQuicAgentCert RPC, narrower than quic_test.go's fakeQuicManager (package
// agent_test) since this one only needs to exercise the authentication-mode plumbing;
// it always reports enabled.
type fakeQuicAgentCertManager struct {
	mgrrpc.UnimplementedManagerServer
	ca     *quictunnel.CA
	mode   string
	podUID string
}

func (f *fakeQuicAgentCertManager) GetQuicAgentCert(
	context.Context, *mgrrpc.SessionInfo,
) (*mgrrpc.QuicAgentCert, error) {
	sni := quicfwd.AgentSNI(f.podUID)
	cert, err := f.ca.MintServerCert(sni)
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := quictunnel.ServerCertToPEM(cert)
	if err != nil {
		return nil, err
	}
	return &mgrrpc.QuicAgentCert{
		Enabled:            true,
		CertPem:            certPEM,
		KeyPem:             keyPEM,
		CaPem:              f.ca.CertPEM(),
		Sni:                sni,
		AuthenticationMode: f.mode,
	}, nil
}

// startFakeQuicAgentCertManager serves fm as a real gRPC server on a loopback TCP port
// and returns a client connected to it, mirroring quic_test.go's startFakeManager.
func startFakeQuicAgentCertManager(t *testing.T, fm *fakeQuicAgentCertManager) mgrrpc.ManagerClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := grpc.NewServer()
	mgrrpc.RegisterManagerServer(svc, fm)
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return mgrrpc.NewManagerClient(conn)
}

// TestRefreshQuicAgentListener_PopulatesFileShareAuthWithoutQuicPort extends the
// RefreshQuicAgentListener coverage in quic_test.go (package agent_test) with the one
// assertion that requires reading fileShareAuth's unexported snapshot and so can't be
// made from outside this package: with AGENT_QUIC_PORT unset, a manager response still
// populates fileShareAuth with the reported authentication mode, while no QUIC listener
// starts.
func TestRefreshQuicAgentListener_PopulatesFileShareAuthWithoutQuicPort(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})

	cfg := &fakeConfig{sidecar: &agentconfig.Sidecar{}, podIP: netip.MustParseAddr("127.0.0.1")}
	st, err := NewState(ctx, cfg)
	require.NoError(t, err)

	svc := grpc.NewServer()
	agentrpc.RegisterAgentServer(svc, st)
	st.SetGRPCServer(svc)
	t.Cleanup(svc.Stop)

	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	fm := &fakeQuicAgentCertManager{ca: ca, mode: "enforcing", podUID: "test-pod-uid"}
	mgrClient := startFakeQuicAgentCertManager(t, fm)
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, mgrClient, semver.Version{})

	st.RefreshQuicAgentListener(ctx, ctx)

	s := st.(*state)
	require.Nil(t, s.quicAgent.listener, "no QUIC listener should start when AGENT_QUIC_PORT is unset")

	snap := s.fileShareAuth.v.Load()
	require.NotNil(t, snap, "fileShareAuth must be populated even though no QUIC listener starts")
	require.Equal(t, "enforcing", snap.mode)
	require.True(t, s.fileShareAuth.enforcing())
}
