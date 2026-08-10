package manager

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// syncBuffer is a mutex-guarded byte buffer. A bare bytes.Buffer isn't safe
// for the logging handler (writing from the watch's background goroutine) to
// share with a test goroutine that polls it via require.Eventually.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Contains(sub string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(sub))
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// loggingContextSync is loggingContext (service_grant_test.go) specialized to
// syncBuffer, for a handler written to from a background goroutine.
func loggingContextSync(ctx context.Context, buf *syncBuffer) context.Context {
	h := handler.NewText(handler.Output(buf), handler.EnabledLevel(clog.LevelTrace))
	return clog.WithLogger(ctx, slog.New(h))
}

// This file covers WatchNamespaces' session validation and streamed set,
// and the authorized-namespace probe WatchWorkloads applies to an
// explicitly named namespace, including its per-session cache.

// fakeServerStream is a minimal grpc.ServerStreamingServer[T], giving a
// test full control over the request context rather than whatever a real
// bufconn round trip would carry.
type fakeServerStream[T any] struct {
	ctx context.Context
	ch  chan *T
}

func newFakeServerStream[T any](ctx context.Context) *fakeServerStream[T] {
	return &fakeServerStream[T]{ctx: ctx, ch: make(chan *T, 16)}
}

func (f *fakeServerStream[T]) Send(m *T) error {
	select {
	case f.ch <- m:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

func (f *fakeServerStream[T]) SetHeader(metadata.MD) error  { return nil }
func (f *fakeServerStream[T]) SendHeader(metadata.MD) error { return nil }
func (f *fakeServerStream[T]) SetTrailer(metadata.MD)       {}
func (f *fakeServerStream[T]) Context() context.Context     { return f.ctx }
func (f *fakeServerStream[T]) SendMsg(any) error            { return nil }
func (f *fakeServerStream[T]) RecvMsg(any) error            { return nil }

// TestWatchNamespaces_UnknownSession covers that a session id the manager has
// never seen is rejected with NotFound, the same as any other streaming
// handler built on ensureClientSession.
func TestWatchNamespaces_UnknownSession(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	stream := newFakeServerStream[rpc.NamespaceList](sctx)
	err := mgr.WatchNamespaces(&rpc.SessionInfo{SessionId: "unknown-session"}, stream)
	req.Error(err)
	req.Equal(codes.NotFound, status.Code(err))
}

// TestWatchNamespaces_WrongOwner covers that a session bound to one
// principal is refused to a caller presenting a different one, mirroring
// ensureClientSession's ClientOwnershipError check.
func TestWatchNamespaces_WrongOwner(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	bob := &auth.Principal{Username: "bob", UID: "bob-uid"}

	aliceInfo := testdata.GetTestClients(t)["alice"]
	sess, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, alice), aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.NamespaceList](auth.WithPrincipal(sctx, bob))
	err = mgr.WatchNamespaces(sess, stream)
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))
}

// TestWatchNamespaces_StreamsManagedSet covers that a valid session
// immediately receives the manager's full managed-namespace set, unfiltered
// by anything the caller is authorized to do in those namespaces.
func TestWatchNamespaces_StreamsManagedSet(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	aliceInfo := testdata.GetTestClients(t)["alice"]
	sess, err := mgr.ArriveAsClient(sctx, aliceInfo)
	req.NoError(err)

	wctx, cancel := context.WithCancel(sctx)
	defer cancel()
	stream := newFakeServerStream[rpc.NamespaceList](wctx)

	errCh := make(chan error, 1)
	go func() { errCh <- mgr.WatchNamespaces(sess, stream) }()

	select {
	case nl := <-stream.ch:
		req.ElementsMatch([]string{"default", "other"}, nl.Namespaces)
	case err := <-errCh:
		t.Fatalf("WatchNamespaces returned before sending anything: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the initial namespace list")
	}

	cancel()
	req.NoError(<-errCh)
}

// TestWatchWorkloads_ExplicitNamespace_Denied_Enforcing: a denied namespace
// probe fails the watch with PermissionDenied and is then cached, so a
// repeat request produces no additional SubjectAccessReview.
func TestWatchWorkloads_ExplicitNamespace_Denied_Enforcing(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModeEnforcing
		e.AuthorizationRequiredGrant = auth.GrantTelepresence
		e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	rec := &sarRecorder{}
	// Only the connect review passes; every attachments review is denied, so
	// the namespace probe itself is what's under test.
	installRecordingSAR(cs, rec, isConnectReview)

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	countAttachReviews := func() int {
		n := 0
		for _, ra := range rec.all() {
			if isAttachmentReview(&ra) {
				n++
			}
		}
		return n
	}

	request := &rpc.WorkloadEventsRequest{SessionInfo: sess, Namespace: "default"}

	err = mgr.WatchWorkloads(request, newFakeServerStream[rpc.WorkloadEventsDelta](pctx))
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))
	req.Equal(1, countAttachReviews(), "one review per enabled workload kind for the first probe")

	err = mgr.WatchWorkloads(request, newFakeServerStream[rpc.WorkloadEventsDelta](pctx))
	req.Error(err)
	req.Equal(codes.PermissionDenied, status.Code(err))
	req.Equal(1, countAttachReviews(), "a cached denial must not trigger another review")
}

// TestWatchWorkloads_ExplicitNamespace_Denied_NotEnforcing: outside
// ModeEnforcing, a denied namespace probe is logged once and the watch
// served anyway; the permitted verdict is cached per session, so a repeat
// watch triggers neither another review nor another warning.
func TestWatchWorkloads_ExplicitNamespace_Denied_NotEnforcing(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
		e.AuthorizationRequiredGrant = auth.GrantTelepresence
		e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	rec := &sarRecorder{}
	installRecordingSAR(cs, rec, isConnectReview)

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	buf := &syncBuffer{}
	pctx := auth.WithPrincipal(loggingContextSync(sctx, buf), principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	countAttachReviews := func() int {
		n := 0
		for _, ra := range rec.all() {
			if isAttachmentReview(&ra) {
				n++
			}
		}
		return n
	}

	request := &rpc.WorkloadEventsRequest{SessionInfo: sess, Namespace: "default"}

	runWatch := func() error {
		wctx, cancel := context.WithCancel(pctx)
		defer cancel()
		stream := newFakeServerStream[rpc.WorkloadEventsDelta](wctx)
		errCh := make(chan error, 1)
		go func() { errCh <- mgr.WatchWorkloads(request, stream) }()
		select {
		case <-stream.ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the initial workload delta")
		}
		cancel()
		return <-errCh
	}

	req.NoError(runWatch(), "a denied probe outside ModeEnforcing must not fail the watch")
	req.True(buf.Contains("not enforced"), "a denied namespace probe must warn rather than block the watch")
	req.Equal(1, countAttachReviews(), "one review per enabled workload kind for the first probe")

	buf.Reset()
	req.NoError(runWatch())
	req.Equal(1, countAttachReviews(), "a cached verdict must not trigger another review")
	req.False(buf.Contains("not enforced"), "the cached verdict already warned; a repeat watch must not warn again")
}
