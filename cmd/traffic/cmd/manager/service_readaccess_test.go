package manager

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apps "k8s.io/api/apps/v1"
	auth1 "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"google.golang.org/protobuf/proto"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// This file covers the read authorization of the agent-pod watches,
// WatchSessionEvents, WatchWorkloads, GetAgentConfig and ResolveServicePort.

// readCaller names a caller by the attachment grants it holds, in the
// "default" namespace only.
type readCaller struct {
	name  string
	allow func(ra *auth1.ResourceAttributes) bool
	// visible is what the caller sees of the fixture agents and workloads.
	visible []string
}

func inDefault(ra *auth1.ResourceAttributes) bool { return ra.Namespace == "default" }

var readCallers = []readCaller{
	{
		name:    "no grant",
		allow:   isConnectReview,
		visible: nil,
	},
	{
		name: "namespace-wide grant",
		allow: func(ra *auth1.ResourceAttributes) bool {
			return isConnectReview(ra) || isAttachmentReview(ra) && inDefault(ra)
		},
		visible: []string{"default/demo", "default/hello"},
	},
	{
		name: "grant on one workload",
		allow: func(ra *auth1.ResourceAttributes) bool {
			return isConnectReview(ra) || isAttachmentReview(ra) && inDefault(ra) && (ra.Name == "hello" || ra.Name == "ghost")
		},
		visible: []string{"default/hello"},
	},
}

func readFixtures() []runtime.Object {
	objs := grantFixtures()
	for _, name := range []string{"hello", "demo"} {
		objs = append(objs, &apps.Deployment{
			ObjectMeta: meta.ObjectMeta{Name: name, Namespace: "default"},
			Spec: apps.DeploymentSpec{
				Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: core.PodTemplateSpec{
					ObjectMeta: meta.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       core.PodSpec{Containers: []core.Container{{Name: "app"}}},
				},
			},
		})
	}
	return append(objs, &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "echo", Namespace: "default"},
		Spec: core.ServiceSpec{
			ClusterIP: "10.0.0.42",
			Ports:     []core.ServicePort{{Name: "http", Port: 8080, Protocol: core.ProtocolTCP}},
		},
	})
}

type readEnv struct {
	mgr  Service
	sctx context.Context
	pctx context.Context
	sess *rpc.SessionInfo
	rec  *sarRecorder
	t    *testing.T
}

func newReadEnv(t *testing.T, mode auth.Mode, allow func(ra *auth1.ResourceAttributes) bool) *readEnv {
	t.Helper()
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	_, mgr, sctx := getTestClientConnAndService(ctx, t, readFixtures(), func(e *managerutil.Env) {
		e.AuthenticationMode = mode
		e.AuthorizationRequiredGrant = auth.GrantTelepresence
		e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
	})
	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	require.True(t, ok)
	rec := &sarRecorder{}
	installRecordingSAR(cs, rec, allow)

	pctx := auth.WithPrincipal(sctx, &auth.Principal{Username: "alice", UID: "alice-uid"})
	sess, err := mgr.ArriveAsClient(pctx, testdata.GetTestClients(t)["alice"])
	require.NoError(t, err)
	return &readEnv{mgr: mgr, sctx: sctx, pctx: pctx, sess: sess, rec: rec, t: t}
}

// restoreAgents adds agents default/hello, default/demo and other/hello.
func (e *readEnv) restoreAgents() {
	testAgents := testdata.GetTestAgents(e.t)
	hello := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	hello.PodIp = "10.1.2.3"
	demo := proto.Clone(testAgents["demo1"]).(*rpc.AgentInfo)
	demo.PodIp = "10.1.2.4"
	other := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	other.Namespace = "other"
	other.PodUid = "other-ns-pod-uid"
	other.PodName = "hello-other"
	other.PodIp = "10.1.2.5"
	e.mgr.State().RestoreAgents([]*rpc.AgentInfo{hello, demo, other}, time.Now())
}

// attachmentReviews counts the recorded attachment reviews; named selects
// those that carry a workload name.
func (e *readEnv) attachmentReviews(named bool) int {
	n := 0
	for _, ra := range e.rec.all() {
		if isAttachmentReview(&ra) && (ra.Name != "") == named {
			n++
		}
	}
	return n
}

// collect receives from ch until it has been quiet for the settle time.
func collect[T any](ch <-chan *T) []*T {
	var out []*T
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-time.After(500 * time.Millisecond):
			return out
		}
	}
}

func podNames(delta *rpc.AgentPodInfoDelta, into map[string]struct{}) {
	for _, a := range delta.GetUpserts() {
		into[a.Namespace+"/"+a.WorkloadName] = struct{}{}
	}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runWatch runs watch in the background and returns a stop function that
// cancels the stream and returns the handler's error.
func runWatch[T any](ctx context.Context, watch func(stream *fakeServerStream[T]) error) (*fakeServerStream[T], func() error) {
	wctx, cancel := context.WithCancel(ctx)
	stream := newFakeServerStream[T](wctx)
	errCh := make(chan error, 1)
	go func() { errCh <- watch(stream) }()
	return stream, func() error {
		cancel()
		return <-errCh
	}
}

func TestReadAccess_AgentPodWatches(t *testing.T) {
	namespaces := []string{"default", "other"}
	watches := map[string]func(e *readEnv) []string{
		"WatchAgentPods": func(e *readEnv) []string {
			stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentPodInfoSnapshot]) error {
				return e.mgr.WatchAgentPods(e.sess, s)
			})
			msgs := collect(stream.ch)
			require.NoError(e.t, stop())
			names := map[string]struct{}{}
			if len(msgs) > 0 {
				for _, a := range msgs[len(msgs)-1].Agents {
					names[a.Namespace+"/"+a.WorkloadName] = struct{}{}
				}
			}
			return sortedKeys(names)
		},
		"WatchAgentPodsDelta": func(e *readEnv) []string {
			stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
				return e.mgr.WatchAgentPodsDelta(e.sess, s)
			})
			names := map[string]struct{}{}
			for _, m := range collect(stream.ch) {
				podNames(m, names)
			}
			require.NoError(e.t, stop())
			return sortedKeys(names)
		},
		"WatchAgentPodsInNamespacesDelta": func(e *readEnv) []string {
			stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
				return e.mgr.WatchAgentPodsInNamespacesDelta(&rpc.AgentsRequest{Session: e.sess, Namespaces: namespaces}, s)
			})
			names := map[string]struct{}{}
			for _, m := range collect(stream.ch) {
				podNames(m, names)
			}
			require.NoError(e.t, stop())
			return sortedKeys(names)
		},
		"WatchSessionEvents": func(e *readEnv) []string {
			stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.SessionEventsDelta]) error {
				return e.mgr.WatchSessionEvents(&rpc.SessionEventsRequest{Session: e.sess, Namespaces: namespaces}, s)
			})
			names := map[string]struct{}{}
			for _, m := range collect(stream.ch) {
				if m.AgentPods != nil {
					podNames(m.AgentPods, names)
				}
			}
			require.NoError(e.t, stop())
			return sortedKeys(names)
		},
	}
	for wname, watch := range watches {
		for _, c := range readCallers {
			t.Run(wname+"/"+c.name, func(t *testing.T) {
				e := newReadEnv(t, auth.ModeEnforcing, c.allow)
				e.restoreAgents()
				require.Equal(t, append([]string(nil), c.visible...), append([]string(nil), watch(e)...))
			})
		}
	}
}

// TestReadAccess_ConnectedNamespaceReviewed: the namespace the client connected to
// is reviewed like a named one, in the agent-pod watches and WatchWorkloads.
func TestReadAccess_ConnectedNamespaceReviewed(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, isConnectReview)
	e.restoreAgents()
	stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
		return e.mgr.WatchAgentPodsDelta(e.sess, s)
	})
	names := map[string]struct{}{}
	for _, m := range collect(stream.ch) {
		podNames(m, names)
	}
	require.Empty(t, names)
	require.NoError(t, stop())
	var reviewed bool
	for _, ra := range e.rec.all() {
		reviewed = reviewed || isAttachmentReview(&ra) && ra.Namespace == "default" && ra.Name == ""
	}
	require.True(t, reviewed, "the connected namespace must be reviewed")

	wstream, wstop := runWatch(e.pctx, func(s *fakeServerStream[rpc.WorkloadEventsDelta]) error {
		return e.mgr.WatchWorkloads(&rpc.WorkloadEventsRequest{SessionInfo: e.sess}, s)
	})
	for _, m := range collect(wstream.ch) {
		require.Empty(t, m.Events)
	}
	require.NoError(t, wstop())
}

func TestReadAccess_WatchWorkloads(t *testing.T) {
	for _, c := range readCallers {
		for _, namespace := range []string{"", "default"} {
			t.Run(c.name+"/ns="+namespace, func(t *testing.T) {
				e := newReadEnv(t, auth.ModeEnforcing, c.allow)
				stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.WorkloadEventsDelta]) error {
					return e.mgr.WatchWorkloads(&rpc.WorkloadEventsRequest{SessionInfo: e.sess, Namespace: namespace}, s)
				})
				msgs := collect(stream.ch)
				require.NoError(t, stop(), "a denied caller must get an empty list, not an error")
				require.NotEmpty(t, msgs, "the initial snapshot must always be sent")
				names := map[string]struct{}{}
				for _, m := range msgs {
					for _, ev := range m.Events {
						if n := ev.Workload.Name; n == "hello" || n == "demo" {
							names[ev.Workload.Namespace+"/"+n] = struct{}{}
						}
					}
				}
				require.Equal(t, append([]string(nil), c.visible...), append([]string(nil), sortedKeys(names)...))
			})
		}
	}
}

func TestReadAccess_WatchWorkloads_UnmanagedNamespace(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, isConnectReview)
	err := e.mgr.WatchWorkloads(&rpc.WorkloadEventsRequest{SessionInfo: e.sess, Namespace: "nope"},
		newFakeServerStream[rpc.WorkloadEventsDelta](e.pctx))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// TestReadAccess_GetAgentConfig uses workloads that do not exist, so that an
// authorized call fails on the lookup instead of generating a config.
func TestReadAccess_GetAgentConfig(t *testing.T) {
	wantAllowed := map[string]map[string]bool{
		"no grant":              {},
		"namespace-wide grant":  {"ghost": true, "spook": true},
		"grant on one workload": {"ghost": true},
	}
	for _, c := range readCallers {
		for _, wl := range []string{"ghost", "spook"} {
			t.Run(c.name+"/"+wl, func(t *testing.T) {
				e := newReadEnv(t, auth.ModeEnforcing, c.allow)
				_, err := e.mgr.GetAgentConfig(e.pctx, &rpc.AgentConfigRequest{Session: e.sess, Namespace: "default", Name: wl})
				if wantAllowed[c.name][wl] {
					require.Error(t, err)
					require.NotEqual(t, codes.PermissionDenied, status.Code(err), "%v", err)
				} else {
					require.Equal(t, codes.PermissionDenied, status.Code(err))
				}
			})
		}
	}
}

func TestReadAccess_ResolveServicePort(t *testing.T) {
	wantDenied := map[string]bool{"no grant": true, "grant on one workload": true}
	for _, c := range readCallers {
		t.Run(c.name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, c.allow)
			_, err := e.mgr.ResolveServicePort(e.pctx, &rpc.ResolveServicePortRequest{
				Session: e.sess, Namespace: "default", Service: "echo", Port: "http",
			})
			if wantDenied[c.name] {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestReadAccess_SessionEventsKeepsIntercepts: intercept updates are delivered
// even when every agent pod is filtered out.
func TestReadAccess_SessionEventsKeepsIntercepts(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, isConnectReview)
	e.restoreAgents()
	stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.SessionEventsDelta]) error {
		return e.mgr.WatchSessionEvents(&rpc.SessionEventsRequest{Session: e.sess}, s)
	})
	time.Sleep(200 * time.Millisecond)
	ic := &rpc.InterceptInfo{
		Id: e.sess.SessionId + ":ic",
		Spec: &rpc.InterceptSpec{
			Name: "ic", Client: testdata.GetTestClients(t)["alice"].Name, Agent: "hello", Namespace: "default", Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_WAITING,
		ClientSession: e.sess,
	}
	require.NoError(t, e.mgr.State().RestoreIntercepts(e.sctx, []*rpc.InterceptInfo{ic}, time.Now(), nil))
	var intercepts, pods int
	for _, m := range collect(stream.ch) {
		if m.Intercepts != nil {
			intercepts += len(m.Intercepts.Upserts)
		}
		if m.AgentPods != nil {
			pods += len(m.AgentPods.Upserts)
		}
	}
	require.NoError(t, stop())
	require.Equal(t, 1, intercepts)
	require.Zero(t, pods)
}

// failReviews makes the attachment reviews that match fail while the returned
// flag is set, as when the API server cannot be reached.
func failReviews(e *readEnv, match func(ra *auth1.ResourceAttributes) bool) *atomic.Bool {
	cs := k8sapi.GetK8sInterface(e.sctx).(*fake.Clientset)
	failing := &atomic.Bool{}
	failing.Store(true)
	cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*auth1.SubjectAccessReview)
		ra := review.Spec.ResourceAttributes
		if failing.Load() && ra != nil && isAttachmentReview(ra) && match(ra) {
			return true, nil, errors.New("apiserver unavailable")
		}
		return false, nil, nil
	})
	return failing
}

func isNamespaceReview(ra *auth1.ResourceAttributes) bool { return ra.Name == "" }
func isWorkloadReview(ra *auth1.ResourceAttributes) bool  { return ra.Name != "" }

// watchHandlers starts each watch RPC and returns the channel that receives
// the handler's error, with a stop function.
var watchHandlers = map[string]func(e *readEnv) (<-chan error, func(), func() []string){
	"WatchAgentPods": func(e *readEnv) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.AgentPodInfoSnapshot]) error {
			return e.mgr.WatchAgentPods(e.sess, s)
		}, func(m *rpc.AgentPodInfoSnapshot, into map[string]struct{}) {
			clear(into)
			for _, a := range m.Agents {
				into[a.Namespace+"/"+a.WorkloadName] = struct{}{}
			}
		})
	},
	"WatchAgentPodsDelta": func(e *readEnv) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
			return e.mgr.WatchAgentPodsDelta(e.sess, s)
		}, podNames)
	},
	"WatchAgentPodsInNamespacesDelta": func(e *readEnv) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
			return e.mgr.WatchAgentPodsInNamespacesDelta(&rpc.AgentsRequest{Session: e.sess, Namespaces: []string{"default"}}, s)
		}, podNames)
	},
	"WatchSessionEvents": func(e *readEnv) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.SessionEventsDelta]) error {
			return e.mgr.WatchSessionEvents(&rpc.SessionEventsRequest{Session: e.sess, Namespaces: []string{"default"}}, s)
		}, func(m *rpc.SessionEventsDelta, into map[string]struct{}) {
			if m.AgentPods != nil {
				podNames(m.AgentPods, into)
			}
		})
	},
	"WatchWorkloads": func(e *readEnv) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.WorkloadEventsDelta]) error {
			return e.mgr.WatchWorkloads(&rpc.WorkloadEventsRequest{SessionInfo: e.sess, Namespace: "default"}, s)
		}, func(m *rpc.WorkloadEventsDelta, into map[string]struct{}) {
			for _, ev := range m.Events {
				if n := ev.Workload.Name; n == "hello" || n == "demo" {
					into[ev.Workload.Namespace+"/"+n] = struct{}{}
				}
			}
		})
	},
}

// startWatch runs watch in the background. It returns the handler's error
// channel, a function that cancels the stream, and a function that settles
// and returns the workload names seen so far.
func startWatch[T any](e *readEnv, watch func(*fakeServerStream[T]) error, names func(*T, map[string]struct{})) (<-chan error, func(), func() []string) {
	wctx, cancel := context.WithCancel(e.pctx)
	stream := newFakeServerStream[T](wctx)
	errCh := make(chan error, 1)
	go func() { errCh <- watch(stream) }()
	seen := map[string]struct{}{}
	return errCh, cancel, func() []string {
		for _, m := range collect(stream.ch) {
			names(m, seen)
		}
		return sortedKeys(seen)
	}
}

func waitErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not end")
		return nil
	}
}

// TestReadAccess_NamespaceUnavailable: a namespace review that cannot be
// performed fails each RPC with Unavailable, is not cached, and a retry after
// recovery gets the data.
func TestReadAccess_NamespaceUnavailable(t *testing.T) {
	grant := readCallers[1]
	t.Run("ResolveServicePort", func(t *testing.T) {
		e := newReadEnv(t, auth.ModeEnforcing, grant.allow)
		failing := failReviews(e, isNamespaceReview)
		resolve := func() error {
			_, err := e.mgr.ResolveServicePort(e.pctx, &rpc.ResolveServicePortRequest{
				Session: e.sess, Namespace: "default", Service: "echo", Port: "http",
			})
			return err
		}
		require.Equal(t, codes.Unavailable, status.Code(resolve()))
		failing.Store(false)
		require.NoError(t, resolve())
	})
	for name, start := range watchHandlers {
		t.Run(name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, grant.allow)
			e.restoreAgents()
			failing := failReviews(e, isNamespaceReview)
			errCh, stop, _ := start(e)
			require.Equal(t, codes.Unavailable, status.Code(waitErr(t, errCh)))
			stop()

			failing.Store(false)
			errCh, stop, names := start(e)
			require.Equal(t, []string{"default/demo", "default/hello"}, names())
			stop()
			require.NoError(t, waitErr(t, errCh))
		})
	}
}

// TestReadAccess_WorkloadUnavailable: a workload review that cannot be
// performed ends the watch with Unavailable instead of dropping the workload.
func TestReadAccess_WorkloadUnavailable(t *testing.T) {
	grant := readCallers[2]
	for name, start := range watchHandlers {
		t.Run(name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, grant.allow)
			e.restoreAgents()
			failing := failReviews(e, isWorkloadReview)
			errCh, stop, _ := start(e)
			require.Equal(t, codes.Unavailable, status.Code(waitErr(t, errCh)))
			stop()

			failing.Store(false)
			errCh, stop, names := start(e)
			require.Equal(t, []string{"default/hello"}, names())
			stop()
			require.NoError(t, waitErr(t, errCh))
		})
	}
}

// TestReadAccess_NotEnforcing: outside enforcing mode nothing is filtered, the
// namespace is reviewed once and no workload is reviewed.
func TestReadAccess_NotEnforcing(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModePermissive, auth.ModeDisabled} {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			e.restoreAgents()
			stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentPodInfoDelta]) error {
				return e.mgr.WatchAgentPodsDelta(e.sess, s)
			})
			names := map[string]struct{}{}
			for _, m := range collect(stream.ch) {
				podNames(m, names)
			}
			require.NoError(t, stop())
			require.Equal(t, []string{"default/demo", "default/hello"}, sortedKeys(names))
			require.Zero(t, e.attachmentReviews(true), "no workload review outside enforcing mode")
			require.LessOrEqual(t, e.attachmentReviews(false), 2, "at most one namespace review (create and get)")

			_, err := e.mgr.ResolveServicePort(e.pctx, &rpc.ResolveServicePortRequest{
				Session: e.sess, Namespace: "default", Service: "echo", Port: "http",
			})
			require.NoError(t, err)
			require.LessOrEqual(t, e.attachmentReviews(false), 2, "the verdict is cached per session")
		})
	}
}
