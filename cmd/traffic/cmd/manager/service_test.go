package manager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	fakeDiscovery "k8s.io/client-go/discovery/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	fakeargorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func dumps(o any) string {
	bs, _ := json.Marshal(o)
	return string(bs)
}

func TestConnect(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no bookmark events),
	// which is enabled by default in client-go v0.35+. Disable it for this test.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	require := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

	conn := getTestClientConn(ctx, t, nil)
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	ver, err := client.Version(ctx, &empty.Empty{})
	require.NoError(err)
	require.Equal(version.Version, ver.Version)
	// The test harness's Env leaves AuthenticationMode at its zero value, which
	// behaves as ModePermissive: authentication is supported but not required.
	require.True(ver.AuthSupported)
	require.False(ver.AuthRequired)

	// Alice arrives and departs

	aliceSess1, err := client.ArriveAsClient(ctx, testClients["alice"])
	require.NoError(err)
	t.Logf("aliceSess1: %v", aliceSess1)

	_, err = client.Depart(ctx, aliceSess1)
	require.NoError(err)

	// Alice arrives and sees no agents or intercepts

	aliceSess2, err := client.ArriveAsClient(ctx, testClients["alice"])
	require.NoError(err)
	t.Logf("aliceSess2: %v", aliceSess2)

	t.Log("WatchAgents(aliceSess2)...")
	aliceWA, err := client.WatchAgents(ctx, aliceSess2)
	require.NoError(err)

	aSnapA, err := aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 0)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	t.Log("WatchIntercepts(aliceSess2)...")
	aliceWI, err := client.WatchIntercepts(ctx, aliceSess2)
	require.NoError(err)

	aSnapI, err := aliceWI.Recv()
	require.NoError(err)
	require.Len(aSnapI.Intercepts, 0)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	// Hello's agent arrives

	helloSess, err := client.ArriveAsAgent(ctx, testAgents["hello"])
	require.NoError(err)
	t.Logf("helloSess: %v", helloSess)

	t.Log("WatchIntercepts(helloSess)...")
	helloWI, err := client.WatchIntercepts(ctx, helloSess)
	require.NoError(err)

	hSnapI, err := helloWI.Recv()
	require.NoError(err)
	require.Len(hSnapI.Intercepts, 0)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Alice sees an agent

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 1)
	require.True(proto.Equal(testAgents["hello"], aSnapA.Agents[0]))
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Demo Deployment comes up with two Pods

	demo1Sess, err := client.ArriveAsAgent(ctx, testAgents["demo1"])
	require.NoError(err)
	t.Logf("demo1Sess: %v", demo1Sess)

	demo1WI, err := client.WatchIntercepts(ctx, demo1Sess)
	require.NoError(err)

	d1SnapI, err := demo1WI.Recv()
	require.NoError(err)
	require.Len(d1SnapI.Intercepts, 0)
	t.Logf("=> agent[demo1] interface snapshot = %s", dumps(d1SnapI))

	demo2Sess, err := client.ArriveAsAgent(ctx, testAgents["demo2"])
	require.NoError(err)
	t.Logf("demo2Sess: %v", demo2Sess)

	demo2WI, err := client.WatchIntercepts(ctx, demo2Sess)
	require.NoError(err)

	d2SnapI, err := demo2WI.Recv()
	require.NoError(err)
	require.Len(d2SnapI.Intercepts, 0)
	t.Logf("=> agent[demo2] interface snapshot = %s", dumps(d2SnapI))

	// Alice sees all the agents

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	if len(aSnapA.Agents) == 2 {
		t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))
		t.Logf("=> client[alice] trying again...")
		aSnapA, err = aliceWA.Recv()
		require.NoError(err)
	}
	require.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Alice remains

	_, err = client.Remain(ctx, &rpc.RemainRequest{Session: aliceSess2})
	require.NoError(err)

	// Hello Pro's agent arrives and departs

	helloProSess, err := client.ArriveAsAgent(ctx, testAgents["helloPro"])
	require.NoError(err)
	t.Logf("helloProSess: %v", helloProSess)

	helloProWI, err := client.WatchIntercepts(ctx, helloProSess)
	require.NoError(err)

	hPSnapI, err := helloProWI.Recv()
	require.NoError(err)
	require.Len(hPSnapI.Intercepts, 0)
	t.Logf("=> agent[helloPro] intercept snapshot = %s", dumps(hPSnapI))

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 4)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	_, err = client.Depart(ctx, helloProSess)
	require.NoError(err)

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))
	_, err = client.Depart(ctx, aliceSess2)
	require.NoError(err)
	_, err = client.Depart(ctx, helloSess)
	require.NoError(err)
	_, err = client.Depart(ctx, demo1Sess)
	require.NoError(err)
	_, err = client.Depart(ctx, demo2Sess)
	require.NoError(err)
}

// TestWatchQuicBackends proves the backend-allowlist RPC's subscribe/update
// contract required by the QUIC forwarder design
// (docs/reference/quic-transport-architecture.md, "The forwarder"): an
// immediate initial snapshot containing the
// traffic-manager's own pod IP, and a further, full-replacement snapshot
// whenever an agent session arrives or departs. The call is deliberately made
// with no SessionInfo -- the forwarder has no client session.
func TestWatchQuicBackends(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testAgents := testdata.GetTestAgents(t)

	conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) { e.TunnelQuicPort = 7778 })
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	wqb, err := client.WatchQuicBackends(ctx, &empty.Empty{})
	req.NoError(err)

	// Initial snapshot: just this traffic-manager's own pod IP and port.
	snap, err := wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 1)
	req.Equal("manager", snap.Backends[0].Kind)
	req.Equal(int32(7778), snap.Backends[0].Port)
	mgrIP, ok := netip.AddrFromSlice(snap.Backends[0].Ip)
	req.True(ok)
	req.Equal("10.0.0.9", mgrIP.String())

	// An agent with no QUIC listener of its own (QuicPort == 0) arrives: it
	// must NOT join the allowlist -- a port-0 "backend" cannot be dialed. It
	// arrives immediately before the QUIC-enabled agent below so that both
	// changes coalesce into the single debounced snapshot asserted next; that
	// snapshot must reflect its exclusion.
	noQuicAgent := proto.Clone(testAgents["helloPro"]).(*rpc.AgentInfo)
	noQuicAgent.PodIp = "10.1.2.4"
	_, err = client.ArriveAsAgent(ctx, noQuicAgent)
	req.NoError(err)

	// An agent with a QUIC listener arrives; its pod IP, port, and pod UID all
	// join the allowlist.
	helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	helloAgent.PodIp = "10.1.2.3"
	helloAgent.QuicPort = 7787
	helloSess, err := client.ArriveAsAgent(ctx, helloAgent)
	req.NoError(err)

	snap, err = wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 2)
	var sawManager, sawAgent bool
	for _, b := range snap.Backends {
		ip, ok := netip.AddrFromSlice(b.Ip)
		req.True(ok)
		switch b.Kind {
		case "manager":
			sawManager = true
			req.Equal("10.0.0.9", ip.String())
			req.Equal(int32(7778), b.Port)
		case "agent":
			sawAgent = true
			req.Equal("10.1.2.3", ip.String())
			req.Equal(int32(7787), b.Port)
			req.Equal(helloAgent.PodUid, b.PodUid)
		default:
			t.Fatalf("unexpected backend kind %q", b.Kind)
		}
	}
	req.True(sawManager)
	req.True(sawAgent)

	// The agent departs; the allowlist shrinks back to just the manager.
	_, err = client.Depart(ctx, helloSess)
	req.NoError(err)

	snap, err = wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 1)
	req.Equal("manager", snap.Backends[0].Kind)
}

// TestGetQuicAgentCert covers the cases the RPC's contract distinguishes, per "Agent
// connections over QUIC" in docs/reference/quic-transport-architecture.md: an agent
// session gets a certificate that verifies against the manager's QUIC CA for exactly
// its own SNI name and carries the manager's authentication mode; the QUIC CA is now
// unconditional (see NewService), so a manager with no QUIC tunnel port still returns
// Enabled true and usable material; and a client (non-agent) session is rejected
// outright, regardless of the QUIC tunnel port.
func TestGetQuicAgentCert(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	testAgents := testdata.GetTestAgents(t)
	testClients := testdata.GetTestClients(t)

	verifyCert := func(t *testing.T, cert *rpc.QuicAgentCert, wantSNI string) {
		t.Helper()
		req := require.New(t)
		req.True(cert.Enabled)
		req.Equal(wantSNI, cert.Sni)

		roots := x509.NewCertPool()
		req.True(roots.AppendCertsFromPEM(cert.CaPem))
		pair, err := tls.X509KeyPair(cert.CertPem, cert.KeyPem)
		req.NoError(err)
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		req.NoError(err)
		_, err = leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   wantSNI,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		req.NoError(err, "minted agent certificate must verify against the manager's QUIC CA for its own SNI name")
	}

	t.Run("agent session with QUIC tunnel port", func(t *testing.T) {
		req := require.New(t)
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.AuthenticationMode = auth.ModePermissive
		})
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		agentSess, err := client.ArriveAsAgent(ctx, helloAgent)
		req.NoError(err)

		cert, err := client.GetQuicAgentCert(ctx, agentSess)
		req.NoError(err)
		verifyCert(t, cert, quicfwd.AgentSNI(helloAgent.PodUid))
		req.Equal("permissive", cert.AuthenticationMode)
	})

	t.Run("agent session without a quic tunnel port", func(t *testing.T) {
		req := require.New(t)
		// TunnelQuicPort defaults to 0 here, gating only the QUIC tunnel
		// listener; the QUIC CA itself is unconditional (see NewService), so
		// this RPC still returns Enabled true and usable material.
		conn := getTestClientConn(ctx, t, nil)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		agentSess, err := client.ArriveAsAgent(ctx, helloAgent)
		req.NoError(err)

		cert, err := client.GetQuicAgentCert(ctx, agentSess)
		req.NoError(err)
		verifyCert(t, cert, quicfwd.AgentSNI(helloAgent.PodUid))
	})

	t.Run("non-agent session", func(t *testing.T) {
		req := require.New(t)
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) { e.TunnelQuicPort = 7778 })
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		clientSess, err := client.ArriveAsClient(ctx, testClients["alice"])
		req.NoError(err)

		_, err = client.GetQuicAgentCert(ctx, clientSess)
		req.Error(err)
	})
}

// TestVersion_AuthFlags covers that VersionInfo2's AuthSupported and AuthRequired
// reflect the manager's configured authentication mode.
func TestVersion_AuthFlags(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	tests := []struct {
		name          string
		mode          auth.Mode
		authSupported bool
		authRequired  bool
	}{
		{"disabled", auth.ModeDisabled, false, false},
		{"permissive", auth.ModePermissive, true, false},
		{"enforcing", auth.ModeEnforcing, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
				e.AuthenticationMode = tt.mode
			})
			ver, err := mgr.Version(sctx, &empty.Empty{})
			req.NoError(err)
			req.Equal(tt.authSupported, ver.AuthSupported)
			req.Equal(tt.authRequired, ver.AuthRequired)
		})
	}
}

// agentPrincipal returns the Principal a bound projected ServiceAccount token for
// agent would produce, as auth.NewInterceptor would inject it into ctx.
func agentPrincipal(agent *rpc.AgentInfo) *auth.Principal {
	return &auth.Principal{
		Username: "system:serviceaccount:" + agent.Namespace + ":traffic-agent",
		PodName:  agent.PodName,
		PodUID:   agent.PodUid,
	}
}

// TestArriveAsAgentIsIdempotent covers a retry after the manager has already
// committed the first arrival but its response did not reach the agent.
func TestArriveAsAgentIsIdempotent(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testAgents := testdata.GetTestAgents(t)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	agent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)

	first, err := mgr.ArriveAsAgent(sctx, agent)
	req.NoError(err)
	second, err := mgr.ArriveAsAgent(sctx, agent)
	req.NoError(err)

	req.NotEmpty(first.SessionId)
	req.Equal(first.SessionId, second.SessionId)
	req.Equal(1, mgr.State().CountAgents())
}

// TestAgentSessionBinding covers the pod-identity binding established at agent
// arrival and enforced by ensureAgentSession on later agent-session RPCs: matching
// claims bind the session and lock out every other identity (including no identity
// at all); mismatched claims leave the session unbound, so it keeps working
// permissively, the same as an agent that presents no token at all.
func TestAgentSessionBinding(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testAgents := testdata.GetTestAgents(t)

	t.Run("matching claims bind the session", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		agent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		principal := agentPrincipal(agent)
		sess, err := mgr.ArriveAsAgent(auth.WithPrincipal(sctx, principal), agent)
		req.NoError(err)

		bound := mgr.State().GetAgent(tunnel.SessionID(sess.SessionId)).Principal()
		req.NotNil(bound)
		req.Equal(principal.PodUID, bound.PodUID)

		// The bound pod itself keeps working.
		_, err = mgr.Remain(auth.WithPrincipal(sctx, principal), &rpc.RemainRequest{Session: sess})
		req.NoError(err)

		// A different pod UID is denied, even though it presents a valid token.
		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{
			Username: principal.Username,
			PodName:  "some-other-pod",
			PodUID:   "some-other-uid",
		})
		_, err = mgr.Remain(otherCtx, &rpc.RemainRequest{Session: sess})
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))

		// No principal at all is denied too: a bound session requires claims.
		_, err = mgr.Remain(sctx, &rpc.RemainRequest{Session: sess})
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("mismatched claims leave the session unbound and permissive", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		agent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		mismatched := &auth.Principal{
			Username: "system:serviceaccount:" + agent.Namespace + ":traffic-agent",
			PodName:  "not-" + agent.PodName,
			PodUID:   "not-" + agent.PodUid,
		}
		sess, err := mgr.ArriveAsAgent(auth.WithPrincipal(sctx, mismatched), agent)
		req.NoError(err)
		req.Nil(mgr.State().GetAgent(tunnel.SessionID(sess.SessionId)).Principal())

		_, err = mgr.Remain(sctx, &rpc.RemainRequest{Session: sess})
		req.NoError(err)
	})

	t.Run("old agent with no token anywhere works as before", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		agent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		sess, err := mgr.ArriveAsAgent(sctx, agent)
		req.NoError(err)
		req.Nil(mgr.State().GetAgent(tunnel.SessionID(sess.SessionId)).Principal())

		_, err = mgr.Remain(sctx, &rpc.RemainRequest{Session: sess})
		req.NoError(err)

		_, err = mgr.GetQuicAgentCert(sctx, sess)
		req.NoError(err)
	})
}

// TestAgentSessionBinding_Enforcing covers that ModeEnforcing rejects an agent
// arrival whose bound-token pod claims don't match the presented AgentInfo,
// instead of the permissive warn-and-leave-unbound behavior TestAgentSessionBinding
// exercises for the other modes.
func TestAgentSessionBinding_Enforcing(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testAgents := testdata.GetTestAgents(t)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModeEnforcing
	})

	agent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	mismatched := &auth.Principal{
		Username: "system:serviceaccount:" + agent.Namespace + ":traffic-agent",
		PodName:  "not-" + agent.PodName,
		PodUID:   "not-" + agent.PodUid,
	}
	_, err := mgr.ArriveAsAgent(auth.WithPrincipal(sctx, mismatched), agent)
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))
}

// TestClientSessionBinding covers the caller-identity binding established at
// client arrival and enforced on later client-session RPCs: a session bound to
// one principal can't be driven by another, even across a token rotation that
// keeps the same username and UID; a caller whose token couldn't be verified
// for infrastructure reasons gets Unavailable rather than PermissionDenied,
// since ownership could not be established either way; and a session that
// arrived without a principal (an older client) keeps working permissively.
func TestClientSessionBinding(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testClients := testdata.GetTestClients(t)

	t.Run("owned session locks out every other identity", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		bound := mgr.State().GetClient(tunnel.SessionID(sess.SessionId)).Principal()
		req.NotNil(bound)
		req.Equal(principal.Username, bound.Username)

		// The bound caller itself keeps working.
		_, err = mgr.Remain(auth.WithPrincipal(sctx, principal), &rpc.RemainRequest{Session: sess})
		req.NoError(err)

		// A token rotation that keeps the same username and UID still passes:
		// SameAs compares values, not the Principal instance.
		rotated := &auth.Principal{Username: principal.Username, UID: principal.UID}
		_, err = mgr.Remain(auth.WithPrincipal(sctx, rotated), &rpc.RemainRequest{Session: sess})
		req.NoError(err)

		// A different identity is denied.
		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.Remain(otherCtx, &rpc.RemainRequest{Session: sess})
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))

		// No principal at all is denied too: a bound session requires claims.
		_, err = mgr.Remain(sctx, &rpc.RemainRequest{Session: sess})
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))

		// A caller whose token couldn't be verified for infrastructure reasons
		// gets Unavailable instead: ownership could not be established either way.
		_, err = mgr.Remain(auth.WithAuthUnavailable(sctx), &rpc.RemainRequest{Session: sess})
		req.Error(err)
		req.Equal(codes.Unavailable, status.Code(err))
	})

	t.Run("unowned session stays permissive", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		bob := proto.Clone(testClients["bob"]).(*rpc.ClientInfo)
		sess, err := mgr.ArriveAsClient(sctx, bob)
		req.NoError(err)
		req.Nil(mgr.State().GetClient(tunnel.SessionID(sess.SessionId)).Principal())

		_, err = mgr.Remain(sctx, &rpc.RemainRequest{Session: sess})
		req.NoError(err)

		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.Remain(otherCtx, &rpc.RemainRequest{Session: sess})
		req.NoError(err)
	})

	t.Run("ReconnectClient by a non-owner is denied", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.ReconnectClient(otherCtx, &rpc.ReconnectClientRequest{Session: sess, Client: alice})
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("GetQuicTunnelEndpoint by a non-owner is denied", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicExternalHost = "quic.example.com"
		})

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.GetQuicTunnelEndpoint(otherCtx, sess)
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))
	})
}

// TestSetLogLevel_SessionBinding covers session-based authorization on SetLogLevel:
// a request without a session keeps working for an older client in permissive mode;
// a request that carries a session is denied for a caller that doesn't own it and
// accepted for the owner; a session that doesn't exist is NotFound; and a request
// without a session is Unauthenticated in ModeEnforcing.
func TestSetLogLevel_SessionBinding(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testClients := testdata.GetTestClients(t)

	llReq := func(session *rpc.SessionInfo) *rpc.LogLevelRequest {
		return &rpc.LogLevelRequest{
			LogLevel: "debug",
			Duration: durationpb.New(50 * time.Millisecond),
			Session:  session,
		}
	}

	t.Run("no session succeeds in permissive mode", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		_, err := mgr.SetLogLevel(sctx, llReq(nil))
		req.NoError(err)
	})

	t.Run("session owned by another identity is denied", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.SetLogLevel(otherCtx, llReq(sess))
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("owning identity succeeds", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		_, err = mgr.SetLogLevel(auth.WithPrincipal(sctx, principal), llReq(sess))
		req.NoError(err)
	})

	t.Run("unknown session is not found", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		_, err := mgr.SetLogLevel(sctx, llReq(&rpc.SessionInfo{SessionId: "does-not-exist"}))
		req.Error(err)
		req.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("no session is unauthenticated in enforcing mode", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
			e.AuthenticationMode = auth.ModeEnforcing
		})

		_, err := mgr.SetLogLevel(sctx, llReq(nil))
		req.Error(err)
		req.Equal(codes.Unauthenticated, status.Code(err))
	})
}

// TestCreateIntercept_AuthorizationIsAuditOnly covers that a caller whose RBAC
// denies pods/portforward in the target namespace still has its intercept
// created: SubjectAccessReview authorization is observed, not yet enforced.
func TestCreateIntercept_AuthorizationIsAuditOnly(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	const ns = "default"
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test-agent"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent-abc123", Namespace: ns, Labels: map[string]string{"app": "test-agent"}},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}

	_, mgr, sctx := getTestClientConnAndService(ctx, t, []runtime.Object{dep, pod}, func(e *managerutil.Env) {
		e.AgentArrivalTimeout = 5 * time.Second
	})

	// Every SubjectAccessReview -- namespace-wide and pod-scoped alike -- is denied.
	k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), nil)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	aliceInfo := proto.Clone(testdata.GetTestClients(t)["alice"]).(*rpc.ClientInfo)
	sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, alice), aliceInfo)
	req.NoError(err)

	agentInfo := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
	agentInfo.Name = "test-agent"
	agentInfo.Namespace = ns
	agentInfo.NodeAgent = true
	_, err = mgr.ArriveAsAgent(sctx, agentInfo)
	req.NoError(err)

	ii, err := mgr.CreateIntercept(auth.WithPrincipal(sctx, alice), &rpc.CreateInterceptRequest{
		Session: sess,
		InterceptSpec: &rpc.InterceptSpec{
			Name:         "ic1",
			Client:       aliceInfo.Name,
			Agent:        "test-agent",
			Namespace:    ns,
			WorkloadKind: string(k8sapi.DeploymentKind),
			NodeAgent:    true,
			Mechanism:    "tcp",
		},
	})
	req.NoError(err)
	req.NotNil(ii)
}

// TestCreateIntercept_Enforcing covers that ModeEnforcing rejects an intercept
// whose caller RBAC denies pods/portforward in the target namespace, instead of
// the audit-only behavior TestCreateIntercept_AuthorizationIsAuditOnly exercises
// for the other modes.
func TestCreateIntercept_Enforcing(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	const ns = "default"
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test-agent"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent-abc123", Namespace: ns, Labels: map[string]string{"app": "test-agent"}},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}

	_, mgr, sctx := getTestClientConnAndService(ctx, t, []runtime.Object{dep, pod}, func(e *managerutil.Env) {
		e.AgentArrivalTimeout = 5 * time.Second
		e.AuthenticationMode = auth.ModeEnforcing
	})

	// Deny every review in the target namespace but allow the connect
	// review, so ArriveAsClient succeeds and the intercept is denied.
	mgrNs := managerutil.GetEnv(sctx).ManagerNamespace
	k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), func(_ string, ra *authv1.ResourceAttributes) bool {
		return ra.Namespace == mgrNs
	})

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	aliceInfo := proto.Clone(testdata.GetTestClients(t)["alice"]).(*rpc.ClientInfo)
	sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, alice), aliceInfo)
	req.NoError(err)

	agentInfo := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
	agentInfo.Name = "test-agent"
	agentInfo.Namespace = ns
	agentInfo.NodeAgent = true
	_, err = mgr.ArriveAsAgent(sctx, agentInfo)
	req.NoError(err)

	_, err = mgr.CreateIntercept(auth.WithPrincipal(sctx, alice), &rpc.CreateInterceptRequest{
		Session: sess,
		InterceptSpec: &rpc.InterceptSpec{
			Name:         "ic1",
			Client:       aliceInfo.Name,
			Agent:        "test-agent",
			Namespace:    ns,
			WorkloadKind: string(k8sapi.DeploymentKind),
			NodeAgent:    true,
			Mechanism:    "tcp",
		},
	})
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))
}

// TestResolveServicePort covers resolving a service port by name or number,
// with protocol disambiguation, and the error mapping for an unknown port,
// an unknown service, and a headless service.
func TestResolveServicePort(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	const ns = "default"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.42",
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
				{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP},
			},
		},
	}
	headless := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: ns},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Ports:     []corev1.ServicePort{{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP}},
		},
	}

	_, mgr, sctx := getTestClientConnAndService(ctx, t, []runtime.Object{svc, headless})

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	aliceInfo := proto.Clone(testdata.GetTestClients(t)["alice"]).(*rpc.ClientInfo)
	sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, alice), aliceInfo)
	req.NoError(err)

	tests := []struct {
		name        string
		service     string
		port        string
		protocol    string
		wantPort    int32
		wantErrCode codes.Code
	}{
		{name: "by name TCP", service: "echo", port: "http", wantPort: 8080},
		{name: "by number", service: "echo", port: "8080", wantPort: 8080},
		{name: "by name UDP", service: "echo", port: "dns", protocol: "UDP", wantPort: 53},
		{name: "unknown port name", service: "echo", port: "nope", wantErrCode: codes.FailedPrecondition},
		{name: "unknown service", service: "nonexistent", port: "http", wantErrCode: codes.NotFound},
		{name: "headless service", service: "headless", port: "http", wantErrCode: codes.FailedPrecondition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			rsp, err := mgr.ResolveServicePort(auth.WithPrincipal(sctx, alice), &rpc.ResolveServicePortRequest{
				Session:   sess,
				Namespace: ns,
				Service:   tt.service,
				Port:      tt.port,
				Protocol:  tt.protocol,
			})
			if tt.wantErrCode != codes.OK {
				r.Error(err)
				r.Equal(tt.wantErrCode, status.Code(err))
				return
			}
			r.NoError(err)
			var ip netip.Addr
			r.NoError(ip.UnmarshalBinary(rsp.ClusterIp))
			r.Equal("10.0.0.42", ip.String())
			r.Equal(tt.wantPort, rsp.Port)
		})
	}
}

// TestGetQuicTunnelEndpoint_Gating covers the three cases "Zero-configuration endpoint
// discovery" (docs/reference/quic-transport-architecture.md) distinguishes: an explicit
// externalHost override always wins and bypasses discovery outright; discovery
// candidates alone are sufficient to enable the endpoint when no override is set; and
// neither present means Enabled stays false.
func TestGetQuicTunnelEndpoint_Gating(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testClients := testdata.GetTestClients(t)

	arriveAndFetch := func(t *testing.T, conn *grpc.ClientConn) *rpc.QuicTunnelEndpoint {
		t.Helper()
		req := require.New(t)
		client := rpc.NewManagerClient(conn)
		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		req.NoError(err)
		ep, err := client.GetQuicTunnelEndpoint(ctx, sess)
		req.NoError(err)
		return ep
	}

	t.Run("explicit override, no discovery", func(t *testing.T) {
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicExternalHost = "quic.example.com"
			// A discovered candidate must be ignored in favor of the override:
			// deliberately do NOT set TunnelQuicServiceName, so if the gate ever
			// regressed to consulting discovery first this would fail loudly
			// (Enabled would be false) rather than silently picking the wrong host.
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.True(t, ep.Enabled)
		require.Equal(t, "quic.example.com", ep.Host)
		require.Equal(t, int32(7778), ep.Port)
		require.Len(t, ep.Candidates, 1)
		require.Equal(t, "quic.example.com", ep.Candidates[0].Host)
	})

	t.Run("discovery candidates present, no override", func(t *testing.T) {
		mgrNs := "ambassador"
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager-quic", Namespace: mgrNs},
			Spec: corev1.ServiceSpec{
				Type:  corev1.ServiceTypeLoadBalancer,
				Ports: []corev1.ServicePort{{Port: 7778}},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.9"}},
				},
			},
		}
		conn := getTestClientConn(ctx, t, []runtime.Object{svc}, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicServiceName = "traffic-manager-quic"
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.True(t, ep.Enabled)
		require.Equal(t, "203.0.113.9", ep.Host)
		require.Equal(t, int32(7778), ep.Port)
		require.Len(t, ep.Candidates, 1)
		require.Equal(t, "203.0.113.9", ep.Candidates[0].Host)
	})

	t.Run("neither override nor discovery candidates", func(t *testing.T) {
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicServiceName = "traffic-manager-quic"
			// No Service seeded in the fake clientset: discovery starts (the
			// service name is set) but finds nothing, and there is no override.
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.False(t, ep.Enabled)
	})
}

// TestGetSessionCredential covers GetSessionCredential's contract: the session's
// owner receives a credential -- client certificate and signed token -- that both
// name the session and verify against the returned CA, and this works with no QUIC
// tunnel port configured, since the QUIC CA is now unconditional (see NewService); a
// caller bound to a different identity is denied; and an unknown session is NotFound.
func TestGetSessionCredential(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testClients := testdata.GetTestClients(t)

	t.Run("owner receives a usable credential with no quic tunnel port", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		before := time.Now()
		cred, err := mgr.GetSessionCredential(auth.WithPrincipal(sctx, principal), sess)
		req.NoError(err)
		req.True(cred.Expiry.AsTime().After(before), "expiry must be in the future")

		pair, err := tls.X509KeyPair(cred.ClientCertPem, cred.ClientKeyPem)
		req.NoError(err)
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		req.NoError(err)
		req.Equal(sess.SessionId, leaf.Subject.CommonName)

		roots := x509.NewCertPool()
		req.True(roots.AppendCertsFromPEM(cred.CaPem))
		_, err = leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		req.NoError(err, "minted client certificate must verify against the returned CA")

		pub, err := sessiontoken.PublicKeyFromCertPEM(cred.CaPem)
		req.NoError(err)
		sessionID, err := sessiontoken.Verify(pub, cred.Token, time.Now())
		req.NoError(err)
		req.Equal(sess.SessionId, sessionID)
	})

	t.Run("caller with a different bound identity is denied", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		alice := proto.Clone(testClients["alice"]).(*rpc.ClientInfo)
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), alice)
		req.NoError(err)

		otherCtx := auth.WithPrincipal(sctx, &auth.Principal{Username: "mallory", UID: "mallory-uid"})
		_, err = mgr.GetSessionCredential(otherCtx, sess)
		req.Error(err)
		req.Equal(codes.PermissionDenied, status.Code(err))
	})

	t.Run("unknown session is not found", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

		_, err := mgr.GetSessionCredential(sctx, &rpc.SessionInfo{SessionId: "does-not-exist"})
		req.Error(err)
		req.Equal(codes.NotFound, status.Code(err))
	})
}

func getTestClientConn(ctx context.Context, t *testing.T, extraObjects []runtime.Object, envMods ...func(*managerutil.Env)) *grpc.ClientConn {
	conn, _, _ := getTestClientConnAndService(ctx, t, extraObjects, envMods...)
	return conn
}

// getTestClientConnAndService is identical to getTestClientConn, but also
// returns the manager Service (for direct state access, e.g. RestoreIntercepts)
// and the context the server runs with (for direct mutator.Map access, e.g.
// marking a pod inactive via mutator.GetMap(ctx).Inactivate).
func getTestClientConnAndService(
	ctx context.Context, t *testing.T, extraObjects []runtime.Object, envMods ...func(*managerutil.Env),
) (*grpc.ClientConn, Service, context.Context) {
	const bufsize = 64 * 1024
	var cancel func()
	ctx, cancel = context.WithCancel(ctx)

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	seedObjects := append([]runtime.Object{&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
			Labels: map[string]string{
				labels.NameLabelKey: "default",
			},
		},
	}}, extraObjects...)
	fakeClient := fake.NewClientset(seedObjects...)
	k8sapi.InstallFakeSelfSubjectAccessReviews(fakeClient, nil)
	fakeClient.Discovery().(*fakeDiscovery.FakeDiscovery).FakedServerVersion = &k8sVersion.Info{
		GitVersion: "v1.30.5",
	}

	const mgrNs = "ambassador"
	_, err := fakeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: mgrNs,
			Labels: map[string]string{
				labels.NameLabelKey: mgrNs,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fakeClient.CoreV1().ConfigMaps(mgrNs).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentconfig.ManagerAppName,
			Namespace: mgrNs,
		},
		Data: map[string]string{
			"namespace-selector.yaml": `
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: In
  values:
    - default
    - other
`,
			"agent-state.yaml": ``,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")

	configWatcher := config.NewWatcher(mgrNs)
	go func() {
		if err := configWatcher.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err = configWatcher.ForceEvent(ctx); err != nil {
		t.Fatal(err)
	}
	ctx, err = namespaces.InitContext(ctx, configWatcher.SelectorChannel())
	if err != nil {
		t.Fatal(err)
	}

	f := informer.GetK8sFactory(ctx, "")
	f.Core().V1().Services().Informer()
	f.Core().V1().ConfigMaps().Informer()
	f.Core().V1().Pods().Informer()
	f.Apps().V1().Deployments().Informer()
	f.Apps().V1().StatefulSets().Informer()
	f.Apps().V1().ReplicaSets().Informer()
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())

	env := managerutil.Env{
		ManagerNamespace:   mgrNs,
		GrpcMaxReceiveSize: resource.Quantity{},
		PodCidrStrategy:    "environment",
		PodCidrs: []netip.Prefix{
			netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, 0, 0}), 16),
		},
		PodIp:                     netip.AddrFrom4([4]byte{10, 0, 0, 9}),
		AgentInitContainerEnabled: true,
		AgentMaxIdleTime:          24 * time.Hour,
		ClientConnectionTTL:       24 * time.Minute,
	}
	for _, mod := range envMods {
		mod(&env)
	}
	ctx = managerutil.WithEnv(ctx, &env)
	ctx = mutator.WithMap(ctx, mutator.Load(ctx))

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	s := server.New(ctx)
	g := log.NewGroup(ctx)
	mgr, err := NewService(ctx, g, configWatcher)
	if err != nil {
		t.Fatalf("failed to build manager: %v", err)
	}
	mgr.RegisterServers(s)
	err = configWatcher.ForceEvent(ctx)
	if err != nil {
		t.Fatalf("configMap watcher failed: %v", err)
	}

	g.Go("server", func(ctx context.Context) error {
		defer cancel()
		return s.Serve(lis)
	})
	t.Cleanup(func() {
		s.GracefulStop()
		// Serve races harmlessly with GracefulStop when a test never dials the
		// listener (e.g. it calls the Service's methods directly): the server
		// goroutine may not have reached Serve yet when GracefulStop runs.
		if err := g.Wait(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Error(err)
		}
	})
	return conn, mgr, ctx
}

func Test_hasDomainSuffix(t *testing.T) {
	tests := []struct {
		name   string
		qn     string
		suffix string
		want   bool
	}{
		{
			"empty suffix",
			"aa.bb.",
			"",
			false,
		},
		{
			"suffix with dot",
			"aa.bb.",
			"bb.",
			true,
		},
		{
			"suffix without dot",
			"aa.bb.",
			"bb",
			true,
		},
		{
			"suffix partial match",
			"aa.bb.",
			"b.",
			false,
		},
		{
			"suffix partial match no dot",
			"foo.bar.",
			"b",
			false,
		},
		{
			"name without dot",
			"aa.bb",
			"bb",
			false,
		},
		{
			"equal",
			"a.",
			"a.",
			true,
		},
		{
			"equal no dot",
			"a.",
			"a",
			true,
		},
		{
			"empty qn",
			".",
			"a",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDomainSuffix(tt.qn, tt.suffix); got != tt.want {
				t.Errorf("hasDomainSuffix() = %v, want %v", got, tt.want)
			}
		})
	}
}
