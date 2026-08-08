package manager

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// This file covers StreamLogs: per-pod BEGIN/data/END framing, the
// gate-independent logs.telepresence.io authorization applied per namespace
// (denial produces error frames rather than aborting the request), the
// unconditional nil-principal refusal that is StreamLogs' one deviation from
// this file's usual permissive-mode posture, the per-session concurrent-
// stream limit, and per-pod byte-cap truncation.

// allowLogsReview allows every logs.telepresence.io SubjectAccessReview,
// regardless of namespace or subresource.
func allowLogsReview(_ string, _ *authv1.ResourceAttributes) bool {
	return true
}

// isLogsReviewFor builds an allowed func for InstallFakeSubjectAccessReviews
// that permits the logs.telepresence.io "get" review only in ns.
func isLogsReviewFor(ns string) func(string, *authv1.ResourceAttributes) bool {
	return func(_ string, ra *authv1.ResourceAttributes) bool {
		return ra.Group == "telepresence.io" && ra.Resource == "logs" && ra.Namespace == ns
	}
}

// drainLogStream runs StreamLogs to completion and returns every frame it
// sent. fakeServerStream's channel is buffered well beyond any of this
// file's frame counts, so by the time StreamLogs returns every frame it sent
// is already sitting in the channel, ready to be drained without racing the
// handler.
func drainLogStream(t *testing.T, mgr Service, request *rpc.StreamLogsRequest, stream *fakeServerStream[rpc.LogChunk]) []*rpc.LogChunk {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- mgr.StreamLogs(request, stream) }()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for StreamLogs to return")
	}

	close(stream.ch)
	var frames []*rpc.LogChunk
	for c := range stream.ch {
		frames = append(frames, c)
	}
	return frames
}

// framesFor returns the subset of frames whose PodNamespace is ns.
func framesFor(frames []*rpc.LogChunk, ns string) []*rpc.LogChunk {
	var out []*rpc.LogChunk
	for _, f := range frames {
		if f.PodNamespace == ns {
			out = append(out, f)
		}
	}
	return out
}

// TestStreamLogs_ManagerPod_BeginDataEnd covers the basic authenticated,
// authorized case: a BEGIN frame, at least one data frame, and a terminal
// END frame for the traffic-manager's own pod.
func TestStreamLogs_ManagerPod_BeginDataEnd(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	k8sapi.InstallFakeSubjectAccessReviews(cs, allowLogsReview)

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.LogChunk](pctx)
	frames := drainLogStream(t, mgr, &rpc.StreamLogsRequest{Session: sess, TrafficManager: true}, stream)

	req.GreaterOrEqual(len(frames), 3, "expected at least BEGIN, one data frame, and END")
	req.Equal(rpc.LogChunk_BEGIN, frames[0].Frame)
	req.Equal(rpc.LogChunk_END, frames[len(frames)-1].Frame)

	var gotData bool
	for _, f := range frames[1 : len(frames)-1] {
		req.Empty(f.GetError(), "the manager pod's log read must not fail")
		if len(f.GetData()) > 0 {
			gotData = true
		}
	}
	req.True(gotData, "expected at least one non-empty data frame")
}

// TestStreamLogs_YamlDenied_LogsStreamWithoutErrorOrManifest covers the
// logs-without-manifests grant: with logs.telepresence.io allowed but its
// yaml subresource denied, the pod's log streams normally and the manifest
// is omitted without an error frame.
func TestStreamLogs_YamlDenied_LogsStreamWithoutErrorOrManifest(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	k8sapi.InstallFakeSubjectAccessReviews(cs, func(_ string, ra *authv1.ResourceAttributes) bool {
		return ra.Group == "telepresence.io" && ra.Resource == "logs" && ra.Subresource == ""
	})

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.LogChunk](pctx)
	frames := drainLogStream(t, mgr, &rpc.StreamLogsRequest{Session: sess, TrafficManager: true, GetPodYaml: true}, stream)

	req.GreaterOrEqual(len(frames), 3, "expected at least BEGIN, one data frame, and END")
	var gotData bool
	for _, f := range frames {
		req.Empty(f.GetError(), "a denied yaml review must not produce an error frame")
		req.Empty(f.GetPodYaml(), "a denied yaml review must omit the manifest")
		if len(f.GetData()) > 0 {
			gotData = true
		}
	}
	req.True(gotData, "the log itself must still stream")
}

// TestStreamLogs_NamespaceDenied_OtherNamespaceStillStreams covers that a
// denied logs.telepresence.io review in one namespace produces error frames
// for that namespace's pods without aborting pods in an authorized
// namespace.
func TestStreamLogs_NamespaceDenied_OtherNamespaceStillStreams(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	agentPod := func(name, ns string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: agentconfig.ContainerName}},
			},
		}
	}

	_, mgr, sctx := getTestClientConnAndService(ctx, t,
		[]runtime.Object{agentPod("agent-default", "default"), agentPod("agent-other", "other")},
		func(e *managerutil.Env) {
			e.AuthenticationMode = auth.ModePermissive
		},
	)

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	// Only the "default" namespace's logs review is allowed; "other" is denied.
	k8sapi.InstallFakeSubjectAccessReviews(cs, isLogsReviewFor("default"))

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.LogChunk](pctx)
	frames := drainLogStream(t, mgr, &rpc.StreamLogsRequest{Session: sess, Agents: "all"}, stream)

	allowed := framesFor(frames, "default")
	req.NotEmpty(allowed)
	req.Equal(rpc.LogChunk_BEGIN, allowed[0].Frame)
	req.Equal(rpc.LogChunk_END, allowed[len(allowed)-1].Frame)
	for _, f := range allowed {
		req.Empty(f.GetError(), "the authorized namespace's pod must not carry an error frame")
	}

	denied := framesFor(frames, "other")
	req.NotEmpty(denied)
	req.Equal(rpc.LogChunk_BEGIN, denied[0].Frame)
	req.Equal(rpc.LogChunk_END, denied[len(denied)-1].Frame)
	var gotDenialError bool
	for _, f := range denied {
		if e := f.GetError(); e != "" {
			gotDenialError = true
			req.Contains(e, "not permitted")
		}
	}
	req.True(gotDenialError, "the denied namespace's pod must carry an error frame instead of data")
}

// TestStreamLogs_NilPrincipal_RefusedEvenInPermissiveMode covers StreamLogs'
// deviation from every other authorization call site in this file: a
// session established without a principal (permitted under ModePermissive)
// is still refused Unauthenticated when it tries to stream logs.
func TestStreamLogs_NilPrincipal_RefusedEvenInPermissiveMode(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})

	aliceInfo := testdata.GetTestClients(t)["alice"]
	// No principal on the context: ModePermissive still admits the connect.
	sess, err := mgr.ArriveAsClient(sctx, aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.LogChunk](sctx)
	err = mgr.StreamLogs(&rpc.StreamLogsRequest{Session: sess, TrafficManager: true}, stream)
	req.Error(err)
	req.Equal(codes.Unauthenticated, status.Code(err))
}

// TestStreamLogs_SessionStreamLimit_ResourceExhausted covers that a second
// concurrent StreamLogs call on a session already at its configured
// concurrency limit is refused with ResourceExhausted.
func TestStreamLogs_SessionStreamLimit_ResourceExhausted(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	k8sapi.InstallFakeSubjectAccessReviews(cs, allowLogsReview)

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	session := mgr.State().GetClient(tunnel.SessionID(sess.SessionId))
	req.NotNil(session)
	release, err := session.BeginLogStream()
	req.NoError(err, "manually occupying the one available slot")
	defer release()

	stream := newFakeServerStream[rpc.LogChunk](pctx)
	err = mgr.StreamLogs(&rpc.StreamLogsRequest{Session: sess, TrafficManager: true}, stream)
	req.Error(err)
	req.Equal(codes.ResourceExhausted, status.Code(err))
}

// TestStreamLogs_ByteCapTruncates covers that a pod's log read stops at the
// configured per-pod byte cap and reports the truncation as a trailing
// error frame rather than silently dropping the remainder.
func TestStreamLogs_ByteCapTruncates(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModePermissive
		e.LogStreamPodByteLimit = resource.MustParse("5")
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)
	k8sapi.InstallFakeSubjectAccessReviews(cs, allowLogsReview)
	cs.PrependReactor("get", "pods/log", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &runtime.Unknown{Raw: []byte("0123456789")}, nil
	})

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)
	aliceInfo := testdata.GetTestClients(t)["alice"]

	sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
	req.NoError(err)

	stream := newFakeServerStream[rpc.LogChunk](pctx)
	frames := drainLogStream(t, mgr, &rpc.StreamLogsRequest{Session: sess, TrafficManager: true}, stream)

	var totalData int
	var gotTruncationError bool
	for _, f := range frames {
		totalData += len(f.GetData())
		if e := f.GetError(); e != "" && strings.Contains(e, "truncat") {
			gotTruncationError = true
		}
	}
	req.Equal(5, totalData, "data must stop exactly at the configured byte cap")
	req.True(gotTruncationError, "expected a trailing error frame naming the truncation")
}
