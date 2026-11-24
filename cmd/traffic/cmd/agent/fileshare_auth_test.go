package agent

// This file lives in package agent (not agent_test, unlike most of this directory's
// tests) because fileShareAuth and its snapshot are both unexported: the
// behavior under test -- what a connection sees, and what ends up in the snapshot -- has
// no exported surface to observe it through from outside the package.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	agentrpc "github.com/telepresenceio/telepresence/rpc/v2/agent"
	mgrrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
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
	a.authorizer.Store(newSessionAuthorizer(filesAccess{}, &mgrrpc.SessionInfo{}))
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

// TestFileShareAuth_ValidatePassword_NilSnapshotRefuses covers the state before any
// manager has supplied credential material: every login is refused.
func TestFileShareAuth_ValidatePassword_NilSnapshotRefuses(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	a := &fileShareAuth{}
	require.False(t, a.ready())
	require.False(t, a.enforcing())
	require.Error(t, a.validatePassword(ctx, "u", "not-a-token"))
	require.Error(t, a.validatePassword(ctx, "u", "anonymous"))

	a.set(generateTestKey(t).Public().(*ecdsa.PublicKey), "permissive")
	require.True(t, a.ready())
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
// supplied credential material: the call is refused with codes.Unavailable.
func TestFileShareAuth_VerifySession_NilSnapshot(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	a := &fileShareAuth{}
	verified, err := a.verifySession(ctx, tunnel.SessionID("session-1"))
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.False(t, verified)

	a.set(generateTestKey(t).Public().(*ecdsa.PublicKey), "permissive")
	verified, err = a.verifySession(ctx, tunnel.SessionID("session-1"))
	require.NoError(t, err)
	require.False(t, verified)
}

// TestIsOwnSftpDestination covers the address classification that decides whether a
// tunnel stream is connected to the in-process SFTP server. The cluster cannot vary the
// node-agent's pod IP and the target pod's IP independently.
func TestIsOwnSftpDestination(t *testing.T) {
	podIP := netip.MustParseAddr("10.42.0.7")
	tests := []struct {
		name string
		dst  string
		want bool
	}{
		{"loopback", "127.0.0.1:2222", true},
		{"loopback v6", "[::1]:2222", true},
		{"unspecified", "0.0.0.0:2222", true},
		{"pod IP", "10.42.0.7:2222", true},
		{"pod IP 4-in-6", "[::ffff:10.42.0.7]:2222", true},
		{"other pod", "10.42.9.9:2222", false},
		{"other port", "127.0.0.1:2223", false},
		{"no port", "127.0.0.1:0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isOwnSftpDestination(netip.MustParseAddrPort(tt.dst), 2222, podIP))
		})
	}
	require.False(t, isOwnSftpDestination(netip.MustParseAddrPort("127.0.0.1:0"), 0, podIP), "port 0 is never the SFTP port")
}

// fakeQuicAgentCertManager is a minimal stand-in for the traffic-manager's
// GetQuicAgentCert RPC, narrower than quic_test.go's fakeQuicManager (package
// agent_test) since this one only needs to exercise the authentication-mode plumbing;
// it always reports enabled.
type fakeQuicAgentCertManager struct {
	mgrrpc.UnimplementedManagerServer
	ca                *quictunnel.CA
	mode              string
	podUID            string
	failuresRemaining int
	hangsRemaining    int
	rpcError          error
	mu                sync.Mutex
	calls             int
}

func (f *fakeQuicAgentCertManager) GetQuicAgentCert(
	ctx context.Context, _ *mgrrpc.SessionInfo,
) (*mgrrpc.QuicAgentCert, error) {
	f.mu.Lock()
	f.calls++
	if f.rpcError != nil {
		err := f.rpcError
		f.mu.Unlock()
		return nil, err
	}
	if f.failuresRemaining > 0 {
		f.failuresRemaining--
		f.mu.Unlock()
		return nil, status.Error(codes.Unavailable, "manager is warming up")
	}
	if f.hangsRemaining > 0 {
		f.hangsRemaining--
		f.mu.Unlock()
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	f.mu.Unlock()

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

func (f *fakeQuicAgentCertManager) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
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

func newFileShareAuthTestState(t *testing.T, ctx context.Context) *state {
	t.Helper()
	cfg := &fakeConfig{sidecar: &agentconfig.Sidecar{}, podIP: netip.MustParseAddr("127.0.0.1")}
	st, err := NewState(ctx, cfg)
	require.NoError(t, err)

	svc := grpc.NewServer()
	agentrpc.RegisterAgentServer(svc, st)
	st.SetGRPCServer(svc)
	t.Cleanup(svc.Stop)
	return st.(*state)
}

type refreshQuicAgentListenerFunc func(context.Context, context.Context) error

func (f refreshQuicAgentListenerFunc) RefreshQuicAgentListener(processCtx, fetchCtx context.Context) error {
	return f(processCtx, fetchCtx)
}

// TestRefreshQuicAgentListener_PopulatesFileShareAuthWithoutQuicPort extends the
// RefreshQuicAgentListener coverage in quic_test.go (package agent_test) with the one
// assertion that requires reading fileShareAuth's unexported snapshot and so can't be
// made from outside this package: with AGENT_QUIC_PORT unset, a manager response still
// populates fileShareAuth with the reported authentication mode, while no QUIC listener
// starts.
func TestRefreshQuicAgentListener_PopulatesFileShareAuthWithoutQuicPort(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	st := newFileShareAuthTestState(t, ctx)

	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	fm := &fakeQuicAgentCertManager{ca: ca, mode: "enforcing", podUID: "test-pod-uid"}
	mgrClient := startFakeQuicAgentCertManager(t, fm)
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, mgrClient, semver.Version{})

	require.NoError(t, st.RefreshQuicAgentListener(ctx, ctx))

	require.Nil(t, st.quicAgent.listener, "no QUIC listener should start when AGENT_QUIC_PORT is unset")

	snap := st.fileShareAuth.v.Load()
	require.NotNil(t, snap, "fileShareAuth must be populated even though no QUIC listener starts")
	require.Equal(t, "enforcing", snap.mode)
	require.True(t, st.fileShareAuth.enforcing())
}

func TestRefreshQuicAgentListenerWithRetry_RecoversFileShareAuth(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	st := newFileShareAuthTestState(t, ctx)

	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	fm := &fakeQuicAgentCertManager{
		ca:                ca,
		mode:              "enforcing",
		podUID:            "test-pod-uid",
		failuresRemaining: 2,
	}
	mgrClient := startFakeQuicAgentCertManager(t, fm)
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, mgrClient, semver.Version{})

	refreshQuicAgentListenerWithRetry(ctx, ctx, st, time.Second, time.Millisecond)

	require.Equal(t, 3, fm.callCount())
	snap := st.fileShareAuth.v.Load()
	require.NotNil(t, snap)
	require.Equal(t, "enforcing", snap.mode)

	token, _, err := ca.MintSessionToken("session-a")
	require.NoError(t, err)
	tokenCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, token))
	verified, err := st.fileShareAuth.verifySession(tokenCtx, tunnel.SessionID("session-a"))
	require.NoError(t, err)
	require.True(t, verified)
}

func TestRefreshQuicAgentListenerWithRetry_Unimplemented(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	st := newFileShareAuthTestState(t, ctx)

	fm := &fakeQuicAgentCertManager{rpcError: status.Error(codes.Unimplemented, "not implemented")}
	mgrClient := startFakeQuicAgentCertManager(t, fm)
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, mgrClient, semver.Version{})

	refreshQuicAgentListenerWithRetry(ctx, ctx, st, time.Second, time.Millisecond)

	require.Equal(t, 1, fm.callCount())
	require.False(t, st.fileShareAuth.ready())
	require.Error(t, st.fileShareAuth.validatePassword(ctx, "user", "anonymous"))
	_, err := st.fileShareAuth.verifySession(ctx, tunnel.SessionID("session-a"))
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestRefreshQuicAgentListenerWithRetry_CancelledSession(t *testing.T) {
	processCtx := context.Background()
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempted := make(chan struct{}, 1)
	calls := 0
	refresher := refreshQuicAgentListenerFunc(func(context.Context, context.Context) error {
		calls++
		attempted <- struct{}{}
		return status.Error(codes.Unavailable, "manager is warming up")
	})
	done := make(chan struct{})
	go func() {
		refreshQuicAgentListenerWithRetry(processCtx, sessionCtx, refresher, time.Second, 200*time.Millisecond)
		close(done)
	}()

	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("retry did not make an initial attempt")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry did not stop after the session ended")
	}
	require.Equal(t, 1, calls)
}

func requireVerifies(t *testing.T, ctx context.Context, st *state, ca *quictunnel.CA, sessionID string) {
	t.Helper()
	token, _, err := ca.MintSessionToken(sessionID)
	require.NoError(t, err)
	tokenCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, token))
	verified, err := st.fileShareAuth.verifySession(tokenCtx, tunnel.SessionID(sessionID))
	require.NoError(t, err)
	require.True(t, verified)
}

func requireRejected(t *testing.T, ctx context.Context, st *state, ca *quictunnel.CA, sessionID string) {
	t.Helper()
	token, _, err := ca.MintSessionToken(sessionID)
	require.NoError(t, err)
	tokenCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, token))
	_, err = st.fileShareAuth.verifySession(tokenCtx, tunnel.SessionID(sessionID))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// A traffic-manager restart replaces the CA. An agent whose first fetch from the new
// manager fails must keep retrying and then accept the new manager's tokens.
func TestRefreshQuicAgentListenerWithRetry_ReplacesStaleKeyAfterManagerRestart(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	st := newFileShareAuthTestState(t, ctx)

	oldCA, err := quictunnel.NewCA()
	require.NoError(t, err)
	oldMgr := startFakeQuicAgentCertManager(t, &fakeQuicAgentCertManager{ca: oldCA, mode: "enforcing", podUID: "test-pod-uid"})
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, oldMgr, semver.Version{})
	require.NoError(t, st.RefreshQuicAgentListener(ctx, ctx))
	requireVerifies(t, ctx, st, oldCA, "client-a")

	newCA, err := quictunnel.NewCA()
	require.NoError(t, err)
	fm := &fakeQuicAgentCertManager{ca: newCA, mode: "enforcing", podUID: "test-pod-uid", failuresRemaining: 1}
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-b"}, startFakeQuicAgentCertManager(t, fm), semver.Version{})
	requireRejected(t, ctx, st, newCA, "client-b")

	refreshQuicAgentListenerWithRetry(ctx, ctx, st, time.Second, time.Millisecond)

	require.Equal(t, 2, fm.callCount())
	requireVerifies(t, ctx, st, newCA, "client-b")
	requireRejected(t, ctx, st, oldCA, "client-a")
}

func TestRefreshQuicAgentListenerWithRetry_RetriesAfterAttemptTimeout(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	st := newFileShareAuthTestState(t, ctx)

	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	fm := &fakeQuicAgentCertManager{ca: ca, mode: "enforcing", podUID: "test-pod-uid", hangsRemaining: 1}
	st.SetManager(&mgrrpc.SessionInfo{SessionId: "session-a"}, startFakeQuicAgentCertManager(t, fm), semver.Version{})

	refreshQuicAgentListenerWithRetry(ctx, ctx, st, 100*time.Millisecond, time.Millisecond)

	require.Equal(t, 2, fm.callCount())
	requireVerifies(t, ctx, st, ca, "session-a")
}

func TestQuicAgentCertRefresher_TriggerCutsBackoff(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	calls := 0
	called := make(chan int, 10)
	refresher := refreshQuicAgentListenerFunc(func(context.Context, context.Context) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		called <- n
		if n == 1 {
			return status.Error(codes.Unavailable, "manager is warming up")
		}
		return nil
	})

	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		quicAgentCertRefresher(ctx, ctx, refresher, trigger, time.Second, time.Hour)
	}()

	require.Equal(t, 1, receiveWithin(t, called))
	// The failed first attempt is now in its 1h backoff; a trigger must cut it short.
	trigger <- struct{}{}
	require.Equal(t, 2, receiveWithin(t, called))

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not return after cancel")
	}
}

func TestQuicAgentCertRefresher_RefetchesOnEachTrigger(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	called := make(chan int, 10)
	var n atomic.Int32
	refresher := refreshQuicAgentListenerFunc(func(context.Context, context.Context) error {
		called <- int(n.Add(1))
		return nil
	})

	trigger := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		quicAgentCertRefresher(ctx, ctx, refresher, trigger, time.Second, time.Hour)
	}()

	trigger <- struct{}{}
	require.Equal(t, 1, receiveWithin(t, called))
	trigger <- struct{}{}
	require.Equal(t, 2, receiveWithin(t, called))

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not return after cancel")
	}
	require.Equal(t, int32(2), n.Load())
}

func receiveWithin(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a refresh call")
		return 0
	}
}

// TestWithoutDebugLogging asserts that the context handed to the FTP server reports debug
// logging as disabled, which keeps the server from echoing received lines such as PASS.
func TestWithoutDebugLogging(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	require.True(t, clog.Enabled(ctx, slog.LevelDebug))
	ctx = withoutDebugLogging(ctx)
	require.False(t, clog.Enabled(ctx, slog.LevelDebug))
	require.True(t, clog.Enabled(ctx, slog.LevelInfo))
	require.False(t, clog.Enabled(clog.With(ctx, "k", "v"), slog.LevelDebug))
}

func TestFileShareAuth_NoCredentialsAcceptsUnauthenticated(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	a := &fileShareAuth{}
	a.setNoCredentials()

	require.True(t, a.ready())
	require.False(t, a.enforcing())
	require.NoError(t, a.validatePassword(ctx, "user", "anonymous"))
	verified, err := a.verifySession(ctx, "some-session")
	require.NoError(t, err)
	require.False(t, verified)
}

// filesAccess is a manager client that grants every session access to the file servers.
type filesAccess struct {
	mgrrpc.ManagerClient
}

func (filesAccess) AuthorizeSessionAccess(context.Context, *mgrrpc.AuthorizeSessionAccessRequest, ...grpc.CallOption) (*mgrrpc.AuthorizeSessionAccessResponse, error) {
	return &mgrrpc.AuthorizeSessionAccessResponse{Level: mgrrpc.AuthorizeSessionAccessResponse_FILES}, nil
}

// noAccessCall is a manager client that doesn't implement AuthorizeSessionAccess.
type noAccessCall struct {
	mgrrpc.ManagerClient
}

func (noAccessCall) AuthorizeSessionAccess(context.Context, *mgrrpc.AuthorizeSessionAccessRequest, ...grpc.CallOption) (*mgrrpc.AuthorizeSessionAccessResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not implemented")
}

// The regression suite AgentidPreAccessCallManager covers this against a real
// traffic-agent; this checks that the refusal is remembered for later requests.
func TestSessionAuthorizer_UnimplementedRefuses(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	sa := newSessionAuthorizer(noAccessCall{}, &mgrrpc.SessionInfo{})
	require.Equal(t, accessNone, sa.level(ctx, "s1", "cred"))
	require.True(t, sa.unimplemented.Load())
	require.Equal(t, accessNone, sa.level(ctx, "s2", "cred"))
}
