package manager

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	empty "google.golang.org/protobuf/types/known/emptypb"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

// This file covers externalService: internal-only methods are refused,
// hardened request forms are rejected while legitimate ones succeed, Version
// needs no principal, and every other method demands one.

// TestExternalService_InternalOnly covers that a method only an agent or the
// quicforwarder calls is refused on the external listener regardless of the
// caller's authentication, with a message naming the internal listener.
func TestExternalService_InternalOnly(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, alice)

	_, err := es.ArriveAsAgent(pctx, &rpc.AgentInfo{})
	req.Error(err)
	req.Equal(codes.Unimplemented, status.Code(err))

	stream := newFakeServerStream[rpc.QuicBackendSnapshot](pctx)
	err = es.WatchQuicBackends(&empty.Empty{}, stream)
	req.Error(err)
	req.Equal(codes.Unimplemented, status.Code(err))
}

// TestExternalService_WatchIntercepts_EmptySession: a blank session id is
// rejected with InvalidArgument rather than reaching the wrapped Service.
func TestExternalService_WatchIntercepts_EmptySession(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, alice)

	stream := newFakeServerStream[rpc.InterceptInfoSnapshot](pctx)
	err := es.WatchIntercepts(&rpc.SessionInfo{SessionId: ""}, stream)
	req.Error(err)
	req.Equal(codes.InvalidArgument, status.Code(err))
}

// TestExternalService_WatchClusterInfo_Ownership: rejected for a caller
// other than the session's owner, accepted for the owner.
func TestExternalService_WatchClusterInfo_Ownership(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	bob := &auth.Principal{Username: "bob", UID: "bob-uid"}
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := es.ArriveAsClient(auth.WithPrincipal(sctx, alice), aliceInfo)
	req.NoError(err)

	otherCtx := auth.WithPrincipal(sctx, bob)
	err = es.WatchClusterInfo(sess, newFakeServerStream[rpc.ClusterInfo](otherCtx))
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))

	wctx, cancel := context.WithCancel(auth.WithPrincipal(sctx, alice))
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- es.WatchClusterInfo(sess, newFakeServerStream[rpc.ClusterInfo](wctx)) }()
	select {
	case err := <-errCh:
		t.Fatalf("WatchClusterInfo returned early for the session's owner: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	req.NoError(<-errCh)
}

// TestExternalService_GetClientConfig_Ownership: requires the caller to
// already own an active client session, and succeeds once it does.
func TestExternalService_GetClientConfig_Ownership(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	carol := &auth.Principal{Username: "carol", UID: "carol-uid"}
	_, err := es.GetClientConfig(auth.WithPrincipal(sctx, carol), &empty.Empty{})
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	aliceInfo := testdata.GetTestClients(t)["alice"]
	pctx := auth.WithPrincipal(sctx, alice)
	_, err = es.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	_, err = es.GetClientConfig(pctx, &empty.Empty{})
	req.NoError(err)
}

// TestExternalService_Version_NoPrincipal covers that Version, the only
// method the external contract serves before authentication, works with no
// principal in the context.
func TestExternalService_Version_NoPrincipal(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	_, err := es.Version(sctx, &empty.Empty{})
	req.NoError(err)
}

// TestExternalService_RequiresPrincipal covers that a post-session method
// other than Version refuses a caller with no principal, defense in depth
// against an unauthenticated call ever reaching a handler.
func TestExternalService_RequiresPrincipal(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)
	es := newExternalService(mgr)

	_, err := es.Remain(sctx, &rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: "does-not-matter"}})
	req.Error(err)
	req.Equal(codes.Unauthenticated, status.Code(err))
}

// TestExternalSurface_UnauthenticatedContract proves the handler-level
// defense in depth: with no auth interceptor installed at all, every method
// on the external surface except Version refuses a caller with no
// principal. A new RPC whose wrapper forgets the check fails here the day
// it is added.
func TestExternalSurface_UnauthenticatedContract(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	lis := bufconn.Listen(64 * 1024)
	gs := server.New(sctx)
	rpc.RegisterManagerServer(gs, newExternalService(mgr))
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	req.NoError(err)
	defer conn.Close()

	refused := func(err error) bool {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.Unimplemented:
			return true
		default:
			return false
		}
	}

	sd := &rpc.Manager_ServiceDesc
	req.Greater(len(sd.Methods)+len(sd.Streams), 30, "the manager surface should be fully enumerated")

	for _, m := range sd.Methods {
		full := "/" + sd.ServiceName + "/" + m.MethodName
		err := conn.Invoke(sctx, full, &empty.Empty{}, &empty.Empty{})
		if m.MethodName == "Version" {
			req.NoError(err, "Version is the deliberately public method")
			continue
		}
		req.True(refused(err), "method %s must refuse an unauthenticated caller, got: %v", m.MethodName, err)
	}

	for _, s := range sd.Streams {
		full := "/" + sd.ServiceName + "/" + s.StreamName
		desc := &grpc.StreamDesc{StreamName: s.StreamName, ServerStreams: s.ServerStreams, ClientStreams: s.ClientStreams}
		cs, err := conn.NewStream(sctx, desc, full)
		req.NoError(err, s.StreamName)
		if !s.ClientStreams {
			req.NoError(cs.SendMsg(&empty.Empty{}), s.StreamName)
			req.NoError(cs.CloseSend(), s.StreamName)
		}
		err = cs.RecvMsg(&empty.Empty{})
		req.True(refused(err), "stream %s must refuse an unauthenticated caller, got: %v", s.StreamName, err)
	}
}
