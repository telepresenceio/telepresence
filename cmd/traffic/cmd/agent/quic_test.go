package agent_test

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	agentrpc "github.com/telepresenceio/telepresence/rpc/v2/agent"
	mgrrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	tpagent "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// fakeQuicManager is a minimal stand-in for the traffic-manager's GetQuicAgentCert RPC,
// mirroring the real implementation in cmd/traffic/cmd/manager/service.go closely enough
// to exercise the agent's fetch-and-swap logic against a real, independently minted CA per
// "Agent connections over QUIC" (docs/reference/quic-transport-architecture.md). Every other Manager
// RPC is unimplemented; RefreshQuicAgentListener never calls any of them.
type fakeQuicManager struct {
	mgrrpc.UnimplementedManagerServer
	ca      *quictunnel.CA
	enabled bool
	podUID  string
}

func (f *fakeQuicManager) GetQuicAgentCert(context.Context, *mgrrpc.SessionInfo) (*mgrrpc.QuicAgentCert, error) {
	if !f.enabled || f.ca == nil {
		return &mgrrpc.QuicAgentCert{Enabled: false}, nil
	}
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
		Enabled: true,
		CertPem: certPEM,
		KeyPem:  keyPEM,
		CaPem:   f.ca.CertPEM(),
		Sni:     sni,
	}, nil
}

// startFakeManager serves fm as a real gRPC server on a loopback TCP port and returns a
// client connected to it, so RefreshQuicAgentListener exercises the exact same
// grpc.ClientConn/grpc.ServerRegistrar path the real manager connection uses.
func startFakeManager(t *testing.T, fm *fakeQuicManager) mgrrpc.ManagerClient {
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

// freeUDPPort returns a currently-unused UDP port by binding to port 0 and immediately
// releasing it. AGENT_QUIC_PORT is always a specific positive port in production (the
// injector only ever sets it when the chart's QUIC port is configured); tests need one
// they don't have to hardcode.
func freeUDPPort(t *testing.T) uint16 {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	require.NoError(t, err)
	defer conn.Close()
	return uint16(conn.LocalAddr().(*net.UDPAddr).Port)
}

// quicStreamConn adapts a *quic.Stream to net.Conn for the gRPC client dialer below. It
// stands in for the client-side stream/net.Conn adapter that a real QUIC client dialer
// (out of scope for the agent-side listener implemented here; see pkg/client/agentpf per
// the design doc) would provide.
type quicStreamConn struct {
	*quic.Stream
	local, remote net.Addr
}

func (c *quicStreamConn) LocalAddr() net.Addr  { return c.local }
func (c *quicStreamConn) RemoteAddr() net.Addr { return c.remote }

// dialWithCA opens a QUIC connection to dialAddr, presenting a client certificate minted
// by ca -- exactly the shape of certificate a client would receive from the manager's own
// session bootstrap RPC (out of scope here).
func dialWithCA(t *testing.T, ctx context.Context, ca *quictunnel.CA, podUID, dialAddr string) (*quic.Conn, error) {
	t.Helper()
	certPEM, keyPEM, err := ca.MintClientCert("quic-test-session")
	require.NoError(t, err)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      ca.Pool(),
		ServerName:   quicfwd.AgentSNI(podUID),
		NextProtos:   []string{tunnel.QuicALPN},
	}
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return quic.DialAddr(dialCtx, dialAddr, tlsConf, nil)
}

// requireDialSucceeds asserts that a client certificate minted by ca is accepted by the
// listener at dialAddr: rejection can arrive either at DialAddr or, since a TLS 1.3
// server doesn't necessarily fail before the handshake nominally completes, on first
// stream use -- so this checks both.
func requireDialSucceeds(t *testing.T, ctx context.Context, ca *quictunnel.CA, podUID, dialAddr string) {
	t.Helper()
	conn, err := dialWithCA(t, ctx, ca, podUID, dialAddr)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()
	qs, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)
	_, err = qs.Write([]byte("x"))
	require.NoError(t, err)
}

// requireDialFails is requireDialSucceeds's negation; see its comment for why both the
// dial and the first stream write are checked.
func requireDialFails(t *testing.T, ctx context.Context, ca *quictunnel.CA, podUID, dialAddr string) {
	t.Helper()
	conn, err := dialWithCA(t, ctx, ca, podUID, dialAddr)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	qs, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return
	}
	if _, err = qs.Write([]byte("x")); err != nil {
		return
	}
	_, err = qs.Read(make([]byte, 1))
	require.Error(t, err, "expected the listener to reject a client certificate signed by a different CA")
}

// newTestAgentState builds a real agent State (the same type Main constructs) wired up
// with a real *grpc.Server that has the agent's own AgentServer implementation
// registered -- exactly as StartServices does in production -- so tests exercise the
// genuine Version RPC rather than a stub.
func newTestAgentState(t *testing.T, quicPort uint16) (ctx context.Context, state tpagent.State) {
	t.Helper()
	env := dos.MapEnv{agentconfig.EnvAgentQuicPort: strconv.Itoa(int(quicPort))}
	ctx = testContext(t, env)

	config, err := tpagent.LoadConfig(ctx)
	require.NoError(t, err)
	state, err = tpagent.NewState(ctx, config)
	require.NoError(t, err)

	svc := grpc.NewServer()
	agentrpc.RegisterAgentServer(svc, state)
	state.SetGRPCServer(svc)
	t.Cleanup(svc.Stop)
	return ctx, state
}

// TestQuicAgentListener_RoundTrip is the full loopback proof: a fake traffic-manager
// serves GetQuicAgentCert with a real CA; RefreshQuicAgentListener fetches from it and
// starts the agent's QUIC listener; a client dials the listener, opens a QUIC stream, and
// drives a real gRPC call (Version) over it through the agent's actual *grpc.Server.
func TestQuicAgentListener_RoundTrip(t *testing.T) {
	quicPort := freeUDPPort(t)
	ctx, state := newTestAgentState(t, quicPort)

	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	mgrClient := startFakeManager(t, &fakeQuicManager{ca: ca, enabled: true, podUID: podUID})
	state.SetManager(&mgrrpc.SessionInfo{SessionId: "quic-test-session"}, mgrClient, semver.Version{})

	state.RefreshQuicAgentListener(ctx, ctx)

	dialAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(quicPort)))
	conn, err := dialWithCA(t, ctx, ca, podUID, dialAddr)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	qs, err := conn.OpenStreamSync(dialCtx)
	require.NoError(t, err)
	sc := &quicStreamConn{Stream: qs, local: conn.LocalAddr(), remote: conn.RemoteAddr()}

	grpcConn, err := grpc.NewClient("passthrough:///agent-quic-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return sc, nil
		}),
	)
	require.NoError(t, err)
	defer func() { _ = grpcConn.Close() }()

	client := agentrpc.NewAgentClient(grpcConn)
	ver, err := client.Version(dialCtx, &emptypb.Empty{})
	require.NoError(t, err)
	require.Equal(t, tpagent.DisplayName, ver.Name)
}

// TestQuicAgentListener_CertSwapWithoutRestart verifies the in-place cert swap:
// a manager reconnect (simulated here by pointing RefreshQuicAgentListener at a
// second fake manager with an independent CA) swaps the running listener's TLS
// material in place -- same address, no restart -- so that clients bearing the new
// CA's certificates are accepted and clients bearing the old CA's are rejected,
// without ever tearing down the QUIC listener.
func TestQuicAgentListener_CertSwapWithoutRestart(t *testing.T) {
	quicPort := freeUDPPort(t)
	ctx, state := newTestAgentState(t, quicPort)
	dialAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(quicPort)))

	ca1, err := quictunnel.NewCA()
	require.NoError(t, err)
	mgr1 := startFakeManager(t, &fakeQuicManager{ca: ca1, enabled: true, podUID: podUID})
	state.SetManager(&mgrrpc.SessionInfo{SessionId: "session-1"}, mgr1, semver.Version{})
	state.RefreshQuicAgentListener(ctx, ctx)

	requireDialSucceeds(t, ctx, ca1, podUID, dialAddr)

	ca2, err := quictunnel.NewCA()
	require.NoError(t, err)
	// Before the "reconnect," a cert from the not-yet-installed CA is rejected.
	requireDialFails(t, ctx, ca2, podUID, dialAddr)

	// Simulate a manager reconnect after a restart: a new manager instance, a new CA.
	// RefreshQuicAgentListener must pick this up via GetConfigForClient, in place.
	mgr2 := startFakeManager(t, &fakeQuicManager{ca: ca2, enabled: true, podUID: podUID})
	state.SetManager(&mgrrpc.SessionInfo{SessionId: "session-2"}, mgr2, semver.Version{})
	state.RefreshQuicAgentListener(ctx, ctx)

	requireDialSucceeds(t, ctx, ca2, podUID, dialAddr)
	requireDialFails(t, ctx, ca1, podUID, dialAddr)
}

// TestQuicAgentListener_Disabled asserts RefreshQuicAgentListener's other documented
// behavior: a manager response of Enabled == false never starts a listener, and does so
// without error (a QUIC-less traffic-manager is a normal, fully supported configuration).
func TestQuicAgentListener_Disabled(t *testing.T) {
	quicPort := freeUDPPort(t)
	ctx, state := newTestAgentState(t, quicPort)

	mgrClient := startFakeManager(t, &fakeQuicManager{enabled: false, podUID: podUID})
	state.SetManager(&mgrrpc.SessionInfo{SessionId: "session-disabled"}, mgrClient, semver.Version{})
	state.RefreshQuicAgentListener(ctx, ctx)

	dialAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(quicPort)))
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err = dialWithCA(t, dialCtx, ca, podUID, dialAddr)
	require.Error(t, err, "no QUIC listener should be running when the manager reports QUIC disabled")
}
