package trafficmgr

import (
	"context"
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/require"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

func newWorkloadSnapshotTestSession(t *testing.T, intercepts map[string]*intercept) *session {
	t.Helper()

	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authorization.SelfSubjectAccessReview)
		review.Status.Allowed = true
		return true, review, nil
	})
	ctx := k8sapi.WithK8sInterface(context.Background(), clientset)
	cluster := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   ctx,
			Namespace: "app-space",
		},
	}
	cluster.SetMappedNamespaces([]string{"app-space", "other"})

	return &session{
		Cluster: cluster,
		// An initialized empty namespace models a watcher with no workload
		// metadata. The intercept-only path must not depend on those rows.
		workloads: map[string]map[workloadInfoKey]workloadInfo{
			"app-space": {},
		},
		currentIngests:    xsync.NewMap[ingestKey, *ingest](),
		currentIntercepts: intercepts,
	}
}

func testIntercept(id, agent, namespace, kind string) *intercept {
	return &intercept{
		InterceptInfo: &manager.InterceptInfo{
			Id: id,
			Spec: &manager.InterceptSpec{
				Agent:        agent,
				Namespace:    namespace,
				WorkloadKind: kind,
			},
		},
	}
}

func TestInterceptOnlySnapshotDoesNotRequireWorkloadInfo(t *testing.T) {
	interceptInfo := testIntercept("intercept-id", "app-replicaset", "app-space", "ReplicaSet")
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{
		interceptInfo.Id: interceptInfo,
	})

	snapshot, err := s.WorkloadInfoSnapshot(
		[]string{"app-space"},
		rpc.ListRequest_INTERCEPTS,
	)

	require.NoError(t, err)
	require.Len(t, snapshot.Workloads, 1)
	require.Equal(t, "app-replicaset", snapshot.Workloads[0].Name)
	require.Equal(t, "app-space", snapshot.Workloads[0].Namespace)
	require.Equal(t, "REPLICASET", snapshot.Workloads[0].WorkloadResourceType)
	require.Equal(t, []*manager.InterceptInfo{interceptInfo.InterceptInfo}, snapshot.Workloads[0].InterceptInfo)
}

func TestInterceptOnlySnapshotEnrichesFromCachedWorkload(t *testing.T) {
	interceptInfo := testIntercept("intercept-id", "app", "app-space", "Deployment")
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{
		interceptInfo.Id: interceptInfo,
	})
	service := &manager.ServiceAssociation{Name: "app"}
	s.workloads["app-space"][workloadInfoKey{
		kind: manager.WorkloadInfo_DEPLOYMENT,
		name: "app",
	}] = workloadInfo{
		uid:             "workload-uid",
		state:           workload.StateProgressing,
		desiredReplicas: 3,
		readyReplicas:   2,
		services:        []*manager.ServiceAssociation{service},
	}
	s.currentAgentPods = []agentPod{{
		workload:  "app",
		namespace: "app-space",
		version:   "2.24.0",
	}}

	snapshot, err := s.WorkloadInfoSnapshot(
		[]string{"app-space"},
		rpc.ListRequest_INTERCEPTS,
	)

	require.NoError(t, err)
	require.Len(t, snapshot.Workloads, 1)
	workload := snapshot.Workloads[0]
	require.Equal(t, "DEPLOYMENT", workload.WorkloadResourceType)
	require.Equal(t, "workload-uid", workload.Uid)
	require.Equal(t, int32(3), workload.DesiredReplicas)
	require.Equal(t, int32(2), workload.ReadyReplicas)
	require.Equal(t, "Progressing", workload.NotInterceptableReason)
	require.Equal(t, "2.24.0", workload.AgentVersion)
	require.Len(t, workload.Services, 1)
	require.Equal(t, "app", workload.Services[0].Name)
	require.NotSame(t, service, workload.Services[0])
}

func TestInterceptOnlySnapshotGroupsAndFiltersIntercepts(t *testing.T) {
	first := testIntercept("a", "app-replicaset", "app-space", "ReplicaSet")
	second := testIntercept("b", "app-replicaset", "app-space", "ReplicaSet")
	anotherWorkload := testIntercept("c", "aardvark", "app-space", "Deployment")
	otherNamespace := testIntercept("d", "app-replicaset", "other", "ReplicaSet")
	replacement := testIntercept("e", "app-replicaset", "app-space", "ReplicaSet")
	replacement.Spec.NoDefaultPort = true
	wiretap := testIntercept("f", "app-replicaset", "app-space", "ReplicaSet")
	wiretap.Spec.Wiretap = true
	nilSpec := &intercept{InterceptInfo: &manager.InterceptInfo{Id: "g"}}

	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{
		first.Id:           first,
		second.Id:          second,
		anotherWorkload.Id: anotherWorkload,
		otherNamespace.Id:  otherNamespace,
		replacement.Id:     replacement,
		wiretap.Id:         wiretap,
		nilSpec.Id:         nilSpec,
		"nil":              nil,
	})

	snapshot, err := s.WorkloadInfoSnapshot(
		[]string{"app-space"},
		rpc.ListRequest_INTERCEPTS,
	)

	require.NoError(t, err)
	require.Len(t, snapshot.Workloads, 2)
	require.Equal(t, "aardvark", snapshot.Workloads[0].Name)
	require.Equal(t, "app-replicaset", snapshot.Workloads[1].Name)
	require.Equal(
		t,
		[]*manager.InterceptInfo{first.InterceptInfo, second.InterceptInfo},
		snapshot.Workloads[1].InterceptInfo,
	)
}

func TestMixedInterceptFilterKeepsWorkloadSnapshotPath(t *testing.T) {
	normal := testIntercept("a", "app-replicaset", "app-space", "ReplicaSet")
	replacement := testIntercept("b", "app-replicaset", "app-space", "ReplicaSet")
	replacement.Spec.NoDefaultPort = true
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{
		normal.Id:      normal,
		replacement.Id: replacement,
	})
	s.workloads["app-space"][workloadInfoKey{
		kind: manager.WorkloadInfo_REPLICASET,
		name: "app-replicaset",
	}] = workloadInfo{}

	snapshot, err := s.WorkloadInfoSnapshot(
		[]string{"app-space"},
		rpc.ListRequest_INTERCEPTS|rpc.ListRequest_REPLACEMENTS,
	)

	require.NoError(t, err)
	require.Len(t, snapshot.Workloads, 1)
	require.Equal(
		t,
		[]*manager.InterceptInfo{normal.InterceptInfo, replacement.InterceptInfo},
		snapshot.Workloads[0].InterceptInfo,
	)
}
