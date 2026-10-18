package state

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func newServiceInterceptState(t *testing.T) (context.Context, *State) {
	t.Helper()
	ctx := testutil.NewContext(t, false)
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	return ctx, &State{
		participantAuthorizer:    allowAllParticipants,
		backgroundCtx:            ctx,
		intercepts:               cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:                   cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:                  xsync.NewMap[tunnel.SessionID, *ClientSession](),
		leases:                   xsync.NewMap[leaseKey, struct{}](),
		workloadWatchers:         xsync.NewMap[string, Watcher](),
		serviceInterceptWatchers: xsync.NewMap[string, struct{}](),
		timedLogLevel:            log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:                   newLoglevelSubscribers(),
	}
}

func allowAllParticipants(context.Context, *auth.Principal, string, string) error { return nil }

func serviceAgent(name, podName, podIP string) *AgentSession {
	return &AgentSession{AgentInfo: &rpc.AgentInfo{
		Name:      name,
		Kind:      "Deployment",
		Namespace: "default",
		PodName:   podName,
		PodIp:     podIP,
		PodUid:    podName + "-uid",
		Mechanisms: []*rpc.AgentInfo_Mechanism{{
			Name:    "http",
			Product: "telepresence",
			Version: "v2.29.2",
		}},
		InterceptTargets: []*rpc.AgentInfo_InterceptTarget{{
			ServiceUid:  "shared-service-uid",
			ServiceName: "example-service",
			ServicePort: 80,
			Protocol:    "TCP",
		}},
	}}
}

func sharedServiceSpec() *rpc.InterceptSpec {
	return &rpc.InterceptSpec{
		Name:          "example-service",
		Client:        "alice",
		Agent:         "example-service",
		WorkloadKind:  "Deployment",
		Namespace:     "default",
		Mechanism:     "http",
		ServiceUid:    "shared-service-uid",
		ServiceName:   "example-service",
		ServicePort:   80,
		Protocol:      "TCP",
		SharedService: true,
	}
}

func sharedServiceWorkloads(names ...string) []*rpc.InterceptWorkload {
	workloads := make([]*rpc.InterceptWorkload, len(names))
	for i, name := range names {
		workloads[i] = &rpc.InterceptWorkload{
			Namespace:    "default",
			WorkloadKind: "Deployment",
			WorkloadName: name,
		}
	}
	return workloads
}

func serviceDeployment(name string, labels map[string]string) *apps.Deployment {
	return &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: "default"},
		Spec: apps.DeploymentSpec{
			Selector: &meta.LabelSelector{MatchLabels: labels},
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: labels},
				Spec: core.PodSpec{Containers: []core.Container{{
					Name:  "app",
					Ports: []core.ContainerPort{{ContainerPort: 8080}},
				}}},
			},
		},
	}
}

func serviceDiscoveryContext(t *testing.T, svc *core.Service, deployments ...*apps.Deployment) context.Context {
	t.Helper()
	ctx := testutil.NewContext(t, false)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fake.NewSimpleClientset(), argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:     "ambassador",
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind},
	})
	f := informer.GetK8sFactory(ctx, "")
	require.NoError(t, f.Core().V1().Services().Informer().GetStore().Add(svc.DeepCopy()))
	for _, deployment := range deployments {
		require.NoError(t, f.Apps().V1().Deployments().Informer().GetStore().Add(deployment.DeepCopy()))
	}
	return ctx
}

func servicePod(name, workload string, labels map[string]string) *core.Pod {
	controller := true
	return &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
			OwnerReferences: []meta.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       workload,
				Controller: &controller,
			}},
		},
	}
}

func serviceWatchContext(t *testing.T, objects ...runtime.Object) (context.Context, *fake.Clientset) {
	t.Helper()
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	ctx, cancel := context.WithCancel(testutil.NewContext(t, false))
	t.Cleanup(cancel)
	client := fake.NewSimpleClientset(objects...)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:     "ambassador",
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind},
	})
	f := informer.GetK8sFactory(ctx, "")
	f.Core().V1().Services().Informer()
	f.Core().V1().Pods().Informer()
	f.Apps().V1().Deployments().Informer()
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())
	return ctx, client
}

func activeServiceIntercept(t *testing.T, ctx context.Context, st *State, stable, canary *AgentSession) *Intercept {
	t.Helper()
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)
	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_WAITING,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(intercept)
	st.intercepts.Store(intercept.Id, intercept)
	intercept = st.ApplyAgentReview(ctx, intercept.Id, stable, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       stable.PodIp,
	})
	intercept = st.ApplyAgentReview(ctx, intercept.Id, canary, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       canary.PodIp,
	})
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)
	return intercept
}

func TestCountActiveInterceptsForServiceParticipantWorkload(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)

	canaryKey := &mutator.WorkloadKey{
		Name:      canary.Name,
		Namespace: canary.Namespace,
		Kind:      k8sapi.DeploymentKind,
	}
	require.Equal(t, 1, st.CountActiveInterceptsForWorkload(canaryKey))
	require.Zero(t, st.CountActiveInterceptsForWorkload(&mutator.WorkloadKey{
		Name:      "unrelated",
		Namespace: canary.Namespace,
		Kind:      k8sapi.DeploymentKind,
	}))

	st.UpdateIntercept(intercept.Id, func(intercept *Intercept) {
		intercept.Disposition = rpc.InterceptDispositionType_WAITING
	})
	require.Zero(t, st.CountActiveInterceptsForWorkload(canaryKey))
}

func TestServiceWorkloadsIncludesSelectedTemplateWithoutReadyPod(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)

	workloads, err := serviceWorkloads(ctx, sharedServiceSpec(), k8sapi.Deployment(stable))
	require.NoError(t, err)
	require.Len(t, workloads, 2)
	require.Equal(t, "example-service", workloads[0].GetName())
	require.Equal(t, "example-service-canary", workloads[1].GetName())
}

func TestServiceInterceptPrunesDroppedSelectorParticipantWithoutAgent(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"track": "stable"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stable, canary), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	stableAgent := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	st.agents.Store("stable", stableAgent)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:   "client:example-service",
		Spec: sharedServiceSpec(),
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", "example-service"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service",
		},
		participantKey("default", "Deployment", "example-service-canary"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service-canary",
		},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	updated := st.reconcileServiceParticipants(intercept.Id, intercept)
	require.Len(t, updated.participants, 1)
	require.Contains(t, updated.participants, participantKey("default", "Deployment", "example-service"))
	require.NotContains(t, updated.participants, participantKey("default", "Deployment", "example-service-canary"))
	require.Len(t, updated.ServiceWorkloads, 1)
}

func TestServiceInterceptFallsBackWhenServiceIdentityChanges(t *testing.T) {
	for _, tt := range []struct {
		name   string
		delete bool
	}{
		{name: "deleted", delete: true},
		{name: "recreated"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
			canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
			svc := &core.Service{
				ObjectMeta: meta.ObjectMeta{
					Name:      "example-service",
					Namespace: "default",
					UID:       k8sTypes.UID("shared-service-uid"),
				},
				Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
			}
			ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
			_, st := newServiceInterceptState(t)
			st.backgroundCtx = ctx
			stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
			canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
			intercept := activeServiceIntercept(t, ctx, st, stable, canary)
			require.True(t, st.IsInterceptedBy(canary, "client"))

			serviceStore := informer.GetK8sFactory(ctx, "").Core().V1().Services().Informer().GetStore()
			if tt.delete {
				require.NoError(t, serviceStore.Delete(svc.DeepCopy()))
			} else {
				recreated := svc.DeepCopy()
				recreated.UID = k8sTypes.UID("replacement-service-uid")
				require.NoError(t, serviceStore.Update(recreated))
			}

			st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

			updated, ok := st.intercepts.Load(intercept.Id)
			require.True(t, ok)
			require.False(t, serviceScopedIntercept(updated.Spec))
			require.Equal(t, "shared-service-uid", updated.Spec.ServiceUid)
			require.Empty(t, updated.participants)
			require.Empty(t, updated.ServiceWorkloads)
			require.False(t, st.IsInterceptedBy(canary, "client"))
		})
	}
}

func TestServiceInterceptFallsBackWhenServiceBecomesSelectorless(t *testing.T) {
	stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)
	require.True(t, st.IsInterceptedBy(canary, "client"))

	selectorless := svc.DeepCopy()
	selectorless.Spec.Selector = nil
	serviceStore := informer.GetK8sFactory(ctx, "").Core().V1().Services().Informer().GetStore()
	require.NoError(t, serviceStore.Update(selectorless))

	st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.False(t, serviceScopedIntercept(updated.Spec))
	require.Equal(t, "shared-service-uid", updated.Spec.ServiceUid)
	require.Empty(t, updated.participants)
	require.Empty(t, updated.ServiceWorkloads)
	require.True(t, st.IsInterceptedBy(stable, "client"))
	require.False(t, st.IsInterceptedBy(canary, "client"))
}

func TestServiceInterceptFallbackRequeuesHealthyPrimary(t *testing.T) {
	for _, tt := range []struct {
		name        string
		disposition rpc.InterceptDispositionType
	}{
		{name: "no agent", disposition: rpc.InterceptDispositionType_NO_AGENT},
		{name: "agent error", disposition: rpc.InterceptDispositionType_AGENT_ERROR},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
			canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
			svc := &core.Service{
				ObjectMeta: meta.ObjectMeta{
					Name:      "example-service",
					Namespace: "default",
					UID:       k8sTypes.UID("shared-service-uid"),
				},
				Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
			}
			ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
			_, st := newServiceInterceptState(t)
			st.backgroundCtx = ctx
			stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
			canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
			intercept := activeServiceIntercept(t, ctx, st, stable, canary)
			st.UpdateIntercept(intercept.Id, func(intercept *Intercept) {
				intercept.Disposition = tt.disposition
				intercept.Message = "Secondary workload did not approve"
			})

			selectorless := svc.DeepCopy()
			selectorless.Spec.Selector = nil
			serviceStore := informer.GetK8sFactory(ctx, "").Core().V1().Services().Informer().GetStore()
			require.NoError(t, serviceStore.Update(selectorless))

			st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

			updated, ok := st.intercepts.Load(intercept.Id)
			require.True(t, ok)
			require.False(t, serviceScopedIntercept(updated.Spec))
			require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
			require.Equal(t, "Waiting for Agent approval", updated.Message)
			require.True(t, AgentMatchesInterceptInfo(stable.AgentInfo, updated))
			require.False(t, AgentMatchesInterceptInfo(canary.AgentInfo, updated))

			updated = st.ApplyAgentReview(ctx, intercept.Id, stable, &rpc.ReviewInterceptRequest{
				Disposition: rpc.InterceptDispositionType_ACTIVE,
				PodIp:       stable.PodIp,
			})
			require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
		})
	}
}

func TestServiceInterceptReselectsReviewWhenPublishedParticipantIsPruned(t *testing.T) {
	stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)
	require.Equal(t, stable.PodName, intercept.PodName)

	updatedService := svc.DeepCopy()
	updatedService.Spec.Selector = map[string]string{"track": "canary"}
	f := informer.GetK8sFactory(ctx, "")
	require.NoError(t, f.Core().V1().Services().Informer().GetStore().Update(updatedService))

	updated := st.reconcileServiceParticipants(intercept.Id, intercept)
	require.Len(t, updated.participants, 1)
	require.NotContains(t, updated.participants, agentParticipantKey(stable.AgentInfo))
	require.Contains(t, updated.participants, agentParticipantKey(canary.AgentInfo))
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
	require.Equal(t, canary.PodIp, updated.PodIp)
	require.Equal(t, canary.PodName, updated.PodName)
	require.False(t, AgentMatchesInterceptInfo(stable.AgentInfo, updated))
	require.False(t, st.IsInterceptedBy(stable, "client"))
	require.True(t, AgentMatchesInterceptInfo(canary.AgentInfo, updated))
	require.True(t, st.IsInterceptedBy(canary, "client"))

	staleStable := serviceAgent("example-service", "stable-pod-2", "10.0.0.3")
	_, err := st.RestoreAgent(ctx, "stale-stable", staleStable.AgentInfo, nil, time.Now())
	require.NoError(t, err)
	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.NotContains(t, updated.participants, agentParticipantKey(staleStable.AgentInfo))
}

func TestRestoreAgentWaitsForServiceWatchBeforeAddingSelectedParticipant(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stable, canary), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	stableAgent := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canaryAgent := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stableAgent)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "client:example-service",
		Spec:        sharedServiceSpec(),
		Disposition: rpc.InterceptDispositionType_ACTIVE,
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", "example-service"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service",
		},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	_, err := st.RestoreAgent(ctx, "canary", canaryAgent.AgentInfo, nil, time.Now())
	require.NoError(t, err)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.NotContains(t, updated.participants, participantKey("default", "Deployment", "example-service-canary"))
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
}

func TestServiceInterceptProvisionsWorkloadSelectedAfterCreation(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	st.agents.Store("stable", serviceAgent("example-service", "stable-pod", "10.0.0.1"))
	st.clients.Store("client", newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now()))

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", "example-service"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service",
		},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Contains(t, updated.participants, participantKey("default", "Deployment", "example-service-canary"))
	require.Equal(t, rpc.InterceptDispositionType_NO_AGENT, updated.Disposition)
	config := mutator.GetMap(ctx).Get("example-service-canary", "default")
	require.NotNil(t, config)
	require.True(t, sidecarClaimsService(config, updated.Spec))
}

func TestServiceInterceptFallsBackForPodOnlySelectionAtCreation(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	pod := servicePod("canary-pod", canary.Name, map[string]string{
		"app":   "example-service",
		"track": "canary",
	})
	require.Empty(t, pod.Status.Conditions)
	ctx, _ := serviceWatchContext(t, svc, stable, canary, pod)
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx

	spec := sharedServiceSpec()
	workloads := st.ensureServiceWorkloads(
		ctx,
		spec,
		k8sapi.Deployment(stable),
		nil,
		nil,
		agentconfig.ReplacePolicyIntercept,
		nil,
	)

	require.Nil(t, workloads)
	require.False(t, serviceScopedIntercept(spec))
	require.Nil(t, mutator.GetMap(ctx).Get(canary.Name, canary.Namespace))
}

func TestServiceInterceptFallsBackForWorkloadSelectedByPodRelabel(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	pod := servicePod("canary-pod", canary.Name, map[string]string{"track": "canary"})
	ctx, client := serviceWatchContext(t, svc, stable, canary, pod)
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	serviceSpec := sharedServiceSpec()
	st.agents.Store("stable", serviceAgent("example-service", "stable-pod", "10.0.0.1"))
	st.clients.Store("client", newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now()))
	existingConfig, err := st.getOrCreateAgentConfig(
		ctx,
		k8sapi.Deployment(canary),
		false,
		false,
		nil,
		agentconfig.ReplacePolicyInactive,
	)
	require.NoError(t, err)
	require.False(t, sidecarClaimsService(existingConfig, serviceSpec))

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", "example-service"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service",
		},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)
	st.startServiceInterceptWatch(intercept.Id)
	require.Eventually(t, func() bool {
		return st.workloadWatchers.Size() == 1
	}, time.Second, 10*time.Millisecond)
	require.False(t, sidecarClaimsService(mutator.GetMap(ctx).Get(canary.Name, canary.Namespace), intercept.Spec))

	currentPod, err := client.CoreV1().Pods("default").Get(ctx, pod.Name, meta.GetOptions{})
	require.NoError(t, err)
	currentPod.Labels["app"] = "example-service"
	_, err = client.CoreV1().Pods("default").Update(ctx, currentPod, meta.UpdateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		updated, ok := st.intercepts.Load(intercept.Id)
		if !ok || serviceScopedIntercept(updated.Spec) || len(updated.participants) != 0 ||
			len(updated.ServiceWorkloads) != 0 {
			return false
		}
		config := mutator.GetMap(ctx).Get(canary.Name, canary.Namespace)
		return config != nil && !sidecarClaimsService(config, serviceSpec)
	}, 5*time.Second, 20*time.Millisecond)
}

func TestServiceInterceptFallsBackForUnrepresentableServicePodOwner(t *testing.T) {
	controller := true
	for _, tt := range []struct {
		name            string
		ownerReferences []meta.OwnerReference
	}{
		{name: "bare pod"},
		{
			name: "unsupported owner",
			ownerReferences: []meta.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "example-job",
				Controller: &controller,
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
			canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
			svc := &core.Service{
				ObjectMeta: meta.ObjectMeta{
					Name:      "example-service",
					Namespace: "default",
					UID:       k8sTypes.UID("shared-service-uid"),
				},
				Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
			}
			pod := &core.Pod{ObjectMeta: meta.ObjectMeta{
				Name:            "unowned-pod",
				Namespace:       "default",
				Labels:          map[string]string{"app": "example-service"},
				OwnerReferences: tt.ownerReferences,
			}}
			ctx, _ := serviceWatchContext(t, svc, stableDeployment, canaryDeployment, pod)
			ctx = mutator.WithMap(ctx, mutator.NewWatcher())
			_, st := newServiceInterceptState(t)
			st.backgroundCtx = ctx
			stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
			canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
			intercept := activeServiceIntercept(t, ctx, st, stable, canary)
			require.True(t, st.IsInterceptedBy(canary, "client"))

			st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

			updated, ok := st.intercepts.Load(intercept.Id)
			require.True(t, ok)
			require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
			require.False(t, serviceScopedIntercept(updated.Spec))
			require.Empty(t, updated.participants)
			require.Empty(t, updated.ServiceWorkloads)
			require.True(t, st.IsInterceptedBy(stable, "client"))
			require.False(t, st.IsInterceptedBy(canary, "client"))
		})
	}
}

func TestServiceInterceptChecksSecondaryConflictsBeforeChangingConfig(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:              "ambassador",
		EnabledWorkloadKinds:          k8sapi.Kinds{k8sapi.DeploymentKind},
		InterceptInactiveBlockTimeout: time.Hour,
	})
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx

	spec := sharedServiceSpec()
	primaryConfig, err := st.getOrCreateAgentConfig(
		ctx,
		k8sapi.Deployment(stable),
		false,
		true,
		spec,
		agentconfig.ReplacePolicyIntercept,
	)
	require.NoError(t, err)
	canaryConfig, err := st.getOrCreateAgentConfig(
		ctx,
		k8sapi.Deployment(canary),
		false,
		false,
		nil,
		agentconfig.ReplacePolicyInactive,
	)
	require.NoError(t, err)
	require.NotEmpty(t, canaryConfig.Containers)
	canaryConfig.Containers[0].Replace = agentconfig.ReplacePolicyContainer

	otherClient := newClientSessionState(ctx, "bob", &rpc.ClientInfo{Name: "bob"}, time.Now())
	st.clients.Store("bob", otherClient)
	st.intercepts.Store("bob:replace", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id: "bob:replace",
		Spec: &rpc.InterceptSpec{
			Name:          "replace",
			Client:        "bob",
			Agent:         canary.Name,
			WorkloadKind:  "Deployment",
			Namespace:     "default",
			Mechanism:     "http",
			Replace:       true,
			ContainerName: "app",
			ContainerPort: 8080,
			Protocol:      "TCP",
		},
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "bob"},
	}})
	client := newClientSessionState(ctx, "alice", &rpc.ClientInfo{Name: "alice"}, time.Now())

	workloads := st.ensureServiceWorkloads(
		ctx,
		spec,
		k8sapi.Deployment(stable),
		primaryConfig,
		nil,
		agentconfig.ReplacePolicyIntercept,
		client,
	)

	require.Nil(t, workloads)
	require.False(t, serviceScopedIntercept(spec))
	config := mutator.GetMap(ctx).Get(canary.Name, canary.Namespace)
	require.NotNil(t, config)
	require.Equal(t, agentconfig.ReplacePolicyContainer, config.Containers[0].Replace)
}

func TestServiceInterceptChecksExistingSecondaryConfigConflictsBeforeJoining(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:              "ambassador",
		EnabledWorkloadKinds:          k8sapi.Kinds{k8sapi.DeploymentKind},
		InterceptInactiveBlockTimeout: time.Hour,
	})
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx

	serviceSpec := sharedServiceSpec()
	canaryConfig, err := st.getOrCreateAgentConfig(
		ctx,
		k8sapi.Deployment(canary),
		false,
		false,
		serviceSpec,
		agentconfig.ReplacePolicyIntercept,
	)
	require.NoError(t, err)
	require.True(t, sidecarClaimsService(canaryConfig, serviceSpec))

	otherClient := newClientSessionState(ctx, "bob", &rpc.ClientInfo{Name: "bob"}, time.Now())
	st.clients.Store("bob", otherClient)
	st.intercepts.Store("bob:replace", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id: "bob:replace",
		Spec: &rpc.InterceptSpec{
			Name:          "replace",
			Client:        "bob",
			Agent:         canary.Name,
			WorkloadKind:  "Deployment",
			Namespace:     "default",
			Mechanism:     "http",
			Replace:       true,
			ContainerName: "app",
			ContainerPort: 8080,
			Protocol:      "TCP",
		},
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "bob"},
	}})
	client := newClientSessionState(ctx, "alice", &rpc.ClientInfo{Name: "alice"}, time.Now())
	st.clients.Store("client", client)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", "example-service"): {
			namespace: "default",
			kind:      "Deployment",
			name:      "example-service",
		},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.False(t, serviceScopedIntercept(updated.Spec))
	require.Empty(t, updated.participants)
	require.Empty(t, updated.ServiceWorkloads)
	require.True(t, sidecarClaimsService(mutator.GetMap(ctx).Get(canary.Name, canary.Namespace), serviceSpec))
}

func TestServiceInterceptParticipantBlocksLaterContainerConflict(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{InterceptInactiveBlockTimeout: time.Hour})
	st.backgroundCtx = ctx
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	existing := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id: "bob:shared",
		Spec: &rpc.InterceptSpec{
			Name:          "shared",
			Client:        "bob",
			Agent:         stable.Name,
			WorkloadKind:  "Deployment",
			Namespace:     "default",
			Mechanism:     "http",
			ServiceUid:    "shared-service-uid",
			ServiceName:   "example-service",
			ServicePort:   80,
			ContainerPort: 8080,
			Protocol:      "TCP",
			SharedService: true,
		},
		Disposition:      rpc.InterceptDispositionType_ACTIVE,
		ClientSession:    &rpc.SessionInfo{SessionId: "bob"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(existing)
	st.intercepts.Store(existing.Id, existing)
	st.clients.Store("bob", newClientSessionState(ctx, "bob", &rpc.ClientInfo{Name: "bob"}, time.Now()))
	client := newClientSessionState(ctx, "alice", &rpc.ClientInfo{Name: "alice"}, time.Now())

	// The same Service port resolves to a different physical container port in
	// the secondary workload. Conflict detection must use this config, not the
	// primary workload's persisted ContainerPort.
	ac := &agentconfig.Sidecar{
		AgentName:    canary.Name,
		WorkloadName: canary.Name,
		WorkloadKind: k8sapi.DeploymentKind,
		Namespace:    canary.Namespace,
		Containers: []*agentconfig.Container{{
			Name: "app",
			Intercepts: []*agentconfig.Intercept{{
				ServiceName:   "example-service",
				ServiceUID:    k8sTypes.UID("shared-service-uid"),
				ServicePort:   80,
				ContainerPort: 9090,
				Protocol:      types.ProtoTCP,
			}},
		}},
	}
	newSpec := &rpc.InterceptSpec{
		Name:          "replace",
		Client:        "alice",
		Agent:         canary.Name,
		WorkloadKind:  "Deployment",
		Namespace:     canary.Namespace,
		Mechanism:     "http",
		Replace:       true,
		ContainerName: "app",
		ContainerPort: 9090,
		Protocol:      "TCP",
	}

	err := st.checkInterceptConflicts(ac, client, []types.PortAndProto{{Proto: types.ProtoTCP, Port: 9090}}, newSpec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "conflict with intercept")

	newSpec.ContainerPort = 9091
	err = st.checkInterceptConflicts(ac, client, []types.PortAndProto{{Proto: types.ProtoTCP, Port: 9091}}, newSpec)
	require.NoError(t, err)
}

func TestServiceInterceptRejectsMixedTargetClaims(t *testing.T) {
	_, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	legacyCanary := serviceAgent("example-service-canary", "canary-pod-2", "10.0.0.3")
	legacyCanary.InterceptTargets = nil
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)
	st.agents.Store("legacy-canary", legacyCanary)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_WAITING,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(intercept)

	code, message := st.checkAgentsForIntercept(intercept)
	require.Equal(t, rpc.InterceptDispositionType_NO_AGENT, code)
	require.Contains(t, message, "Not every agent")
}

func TestServiceInterceptWaitsForEveryWorkload(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_WAITING,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(intercept)
	require.Len(t, intercept.participants, 2)
	require.Len(t, intercept.ServiceWorkloads, 2)
	require.Equal(t, "example-service", intercept.ServiceWorkloads[0].WorkloadName)
	require.Equal(t, "example-service-canary", intercept.ServiceWorkloads[1].WorkloadName)
	st.intercepts.Store(intercept.Id, intercept)

	stableReview := &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       stable.PodIp,
		Environment: map[string]string{"TRACK": "stable"},
	}
	updated := st.ApplyAgentReview(ctx, intercept.Id, stable, stableReview)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
	require.Contains(t, updated.Message, "example-service-canary")

	updated = st.ApplyAgentReview(ctx, intercept.Id, canary, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       canary.PodIp,
		Environment: map[string]string{"TRACK": "canary"},
	})
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
	require.Equal(t, stable.PodIp, updated.PodIp)
	require.Equal(t, stable.PodName, updated.PodName)
	require.Equal(t, map[string]string{"TRACK": "stable"}, updated.Environment)
	require.True(t, st.IsInterceptedBy(stable, "client"))
	require.True(t, st.IsInterceptedBy(canary, "client"))
}

func TestServiceInterceptPublishesLateWorkload(t *testing.T) {
	_, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	st.initializeParticipants(intercept)
	require.Len(t, intercept.ServiceWorkloads, 1)
	require.Equal(t, "example-service", intercept.ServiceWorkloads[0].WorkloadName)

	intercept.addParticipant(canary.AgentInfo)
	require.Len(t, intercept.ServiceWorkloads, 2)
	require.Equal(t, "example-service", intercept.ServiceWorkloads[0].WorkloadName)
	require.Equal(t, "example-service-canary", intercept.ServiceWorkloads[1].WorkloadName)
}

func TestLateServiceParticipantReturnsActiveInterceptToWaiting(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		PodIp:         stable.PodIp,
		PodName:       stable.PodName,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	st.initializeParticipants(intercept)
	stableParticipant := intercept.participants[agentParticipantKey(stable.AgentInfo)]
	require.NotNil(t, stableParticipant)
	stableParticipant.review = &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       stable.PodIp,
	}
	stableParticipant.podName = stable.PodName
	st.intercepts.Store(intercept.Id, intercept)

	_, err := st.RestoreAgent(ctx, "canary", canary.AgentInfo, nil, time.Now())
	require.NoError(t, err)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
	require.Contains(t, updated.Message, "example-service-canary")
	require.Len(t, updated.participants, 2)
	require.Nil(t, updated.participants[agentParticipantKey(canary.AgentInfo)].review)
	require.False(t, st.IsInterceptedBy(stable, "client"))
	require.False(t, st.IsInterceptedBy(canary, "client"))

	updated = st.ApplyAgentReview(ctx, intercept.Id, canary, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       canary.PodIp,
	})
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
}

func TestRestoreInterceptsRebuildsServiceParticipants(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	ctx = k8sapi.WithK8sInterface(ctx, fake.NewSimpleClientset())
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)

	restored := &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: []*rpc.InterceptWorkload{
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: "example-service"},
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: "example-service-canary"},
		},
	}

	st.RestoreIntercepts(ctx, []*rpc.InterceptInfo{restored}, time.Now())
	intercept, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	require.Len(t, intercept.participants, 2)
	require.Len(t, intercept.ServiceWorkloads, 2)
	code, message := st.checkAgentsForIntercept(intercept)
	require.Equal(t, rpc.InterceptDispositionType_UNSPECIFIED, code)
	require.Empty(t, message)
}

func TestRestoreInterceptsWaitsForWatcherBeforeAddingNewSelectedParticipant(t *testing.T) {
	stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"app": "example-service"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	// Keep this test on the restore path itself; the watcher is what may add
	// the new canary after it has preflighted the selection.
	st.serviceInterceptWatchers = nil
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)

	restored := &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_ACTIVE,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name),
	}
	st.RestoreIntercepts(ctx, []*rpc.InterceptInfo{restored}, time.Now())

	intercept, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	require.Len(t, intercept.participants, 1)
	require.NotContains(t, intercept.participants, agentParticipantKey(canary.AgentInfo))
	require.False(t, AgentMatchesInterceptInfo(canary.AgentInfo, intercept))
	require.False(t, st.IsInterceptedBy(canary, "client"))
}

func TestRestoreInterceptsRequeuesWhenSelectorDropsPublishedParticipant(t *testing.T) {
	stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"track": "canary"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	// Keep this test on the restore path itself. The watcher would see the
	// already-pruned selection and cannot repair stale published metadata.
	st.serviceInterceptWatchers = nil
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)

	restored := &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		PodIp:         stable.PodIp,
		PodName:       stable.PodName,
		MountPoint:    "/tmp/stale-mount",
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: []*rpc.InterceptWorkload{
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: stable.Name},
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: canary.Name},
		},
	}

	st.RestoreIntercepts(ctx, []*rpc.InterceptInfo{restored}, time.Now())

	intercept, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, intercept.Disposition)
	require.Empty(t, intercept.PodIp)
	require.Empty(t, intercept.PodName)
	require.Empty(t, intercept.MountPoint)
	require.Len(t, intercept.participants, 1)
	require.NotContains(t, intercept.participants, agentParticipantKey(stable.AgentInfo))
	require.Contains(t, intercept.participants, agentParticipantKey(canary.AgentInfo))
	require.Equal(t, sharedServiceWorkloads(canary.Name), intercept.ServiceWorkloads)
}

func TestRestoreInterceptsRequeuesWhenSelectorDropsUnrestoredPublishedParticipant(t *testing.T) {
	stableDeployment := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canaryDeployment := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{Selector: map[string]string{"track": "stable"}},
	}
	ctx := mutator.WithMap(serviceDiscoveryContext(t, svc, stableDeployment, canaryDeployment), mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	st.serviceInterceptWatchers = nil
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	// The published canary pod disappeared before the restore snapshot, so
	// only the still-selected primary agent is available to restore.
	st.agents.Store("stable", stable)

	restored := &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		PodIp:         canary.PodIp,
		PodName:       canary.PodName,
		MountPoint:    "/tmp/stale-mount",
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: []*rpc.InterceptWorkload{
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: canary.Name},
		},
	}

	st.RestoreIntercepts(ctx, []*rpc.InterceptInfo{restored}, time.Now())

	intercept, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, intercept.Disposition)
	require.Empty(t, intercept.PodIp)
	require.Empty(t, intercept.PodName)
	require.Empty(t, intercept.MountPoint)
	require.Len(t, intercept.participants, 1)
	require.Contains(t, intercept.participants, agentParticipantKey(stable.AgentInfo))
	require.NotContains(t, intercept.participants, agentParticipantKey(canary.AgentInfo))
	require.Equal(t, sharedServiceWorkloads(stable.Name), intercept.ServiceWorkloads)
}

func TestInitializeParticipantsRestoresUnambiguousWaitingPublishedPod(t *testing.T) {
	_, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_WAITING,
		PodIp:            stable.PodIp,
		PodName:          stable.PodName,
		ServiceWorkloads: sharedServiceWorkloads(stable.Name),
	}}

	st.initializeParticipants(intercept)

	participant := intercept.participants[agentParticipantKey(stable.AgentInfo)]
	require.NotNil(t, participant)
	require.Equal(t, stable.PodName, participant.podName)
	require.Equal(t, stable.PodIp, intercept.PodIp)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, intercept.Disposition)
	require.Nil(t, participant.review)
}

func TestInitializeParticipantsClonesRestoredPublishedReview(t *testing.T) {
	_, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	st.agents.Store("stable", stable)
	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_ACTIVE,
		PodIp:            stable.PodIp,
		PodName:          stable.PodName,
		Environment:      map[string]string{"POD": "old"},
		Mounts:           map[string]int32{"/data": 1},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name),
	}}

	st.initializeParticipants(intercept)

	participant := intercept.participants[agentParticipantKey(stable.AgentInfo)]
	require.NotNil(t, participant.review)
	participant.review.Environment["POD"] = "replacement"
	participant.review.Mounts["/data"] = 2
	require.Equal(t, "old", intercept.Environment["POD"])
	require.EqualValues(t, 1, intercept.Mounts["/data"])
}

func TestRestoredServiceInterceptTransfersWhenRecordedPodLeaves(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	ctx = k8sapi.WithK8sInterface(ctx, fake.NewSimpleClientset())
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	stableSibling := serviceAgent("example-service", "stable-pod-2", "10.0.0.3")
	stableSibling.ApiPort = 9901
	stableSibling.FtpPort = 9902
	stableSibling.SftpPort = 9903
	stableSibling.InterceptTargets[0].ContainerName = "app"
	stableSibling.Containers = map[string]*rpc.AgentInfo_ContainerInfo{
		"app": {
			Environment: map[string]string{"POD": "replacement", "EXCLUDED": "must-not-be-published"},
			MountPoint:  "/replacement/mount",
			Mounts:      map[string]int32{"/replacement/data": 1},
		},
	}
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("stable-sibling", stableSibling)
	st.agents.Store("canary", canary)

	restored := &rpc.InterceptInfo{
		Id:                "client:example-service",
		Spec:              sharedServiceSpec(),
		Disposition:       rpc.InterceptDispositionType_ACTIVE,
		PodIp:             stable.PodIp,
		PodName:           stable.PodName,
		FtpPort:           8001,
		SftpPort:          8002,
		MountPoint:        "/old/mount",
		Mounts:            map[string]int32{"/old/data": 2},
		Environment:       map[string]string{"POD": "old"},
		MechanismArgsDesc: "header-filter",
		ClientSession: &rpc.SessionInfo{
			SessionId: "client",
		},
		ServiceWorkloads: []*rpc.InterceptWorkload{
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: "example-service"},
			{Namespace: "default", WorkloadKind: "Deployment", WorkloadName: "example-service-canary"},
		},
	}
	st.RestoreIntercepts(ctx, []*rpc.InterceptInfo{restored}, time.Now())
	intercept, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	participant := intercept.participants[agentParticipantKey(stable.AgentInfo)]
	require.Equal(t, stable.PodName, participant.podName)
	require.NotNil(t, participant.review)
	require.Equal(t, stable.PodIp, participant.review.PodIp)
	require.Equal(t, restored.Environment, participant.review.Environment)
	require.Equal(t, restored.Mounts, participant.review.Mounts)
	require.Nil(t, intercept.participants[agentParticipantKey(canary.AgentInfo)].review)

	st.agents.Delete("stable")
	st.consolidateAgentSessionIntercepts(stable)

	updated, ok := st.intercepts.Load(restored.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
	require.Equal(t, stableSibling.PodName, updated.PodName)
	require.Equal(t, stableSibling.PodIp, updated.PodIp)
	require.Equal(t, stableSibling.ApiPort, updated.ApiPort)
	require.Equal(t, stableSibling.FtpPort, updated.FtpPort)
	require.Equal(t, stableSibling.SftpPort, updated.SftpPort)
	require.Equal(t, "/replacement/mount", updated.MountPoint)
	require.Equal(t, map[string]int32{"/replacement/data": 1}, updated.Mounts)
	require.Equal(t, map[string]string{"POD": "replacement"}, updated.Environment)
	require.Equal(t, "header-filter", updated.MechanismArgsDesc)
	require.Equal(t, map[string]string{"POD": "old"}, restored.Environment)
	participant = updated.participants[agentParticipantKey(stable.AgentInfo)]
	require.NotNil(t, participant.review)
	require.Equal(t, stableSibling.PodName, participant.podName)
	require.Equal(t, stableSibling.PodIp, participant.review.PodIp)
	require.Nil(t, updated.participants[agentParticipantKey(canary.AgentInfo)].review)
}

func TestServiceInterceptRemovalTransfersSecondaryParticipantReview(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	canaryReplacement := serviceAgent("example-service-canary", "canary-pod-2", "10.0.0.3")
	st.agents.Store("stable", stable)
	st.agents.Store("canary", canary)
	st.agents.Store("canary-replacement", canaryReplacement)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             sharedServiceSpec(),
		Disposition:      rpc.InterceptDispositionType_WAITING,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(intercept)
	st.intercepts.Store(intercept.Id, intercept)
	intercept = st.ApplyAgentReview(ctx, intercept.Id, stable, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       stable.PodIp,
		Environment: map[string]string{"PRIMARY": "unchanged"},
	})
	intercept = st.ApplyAgentReview(ctx, intercept.Id, canary, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       canary.PodIp,
	})
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)

	st.agents.Delete("canary")
	st.consolidateAgentSessionIntercepts(canary)

	intercept, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)
	require.Equal(t, stable.PodIp, intercept.PodIp)
	require.Equal(t, stable.PodName, intercept.PodName)
	require.Equal(t, map[string]string{"PRIMARY": "unchanged"}, intercept.Environment)
	participant := intercept.participants[agentParticipantKey(canary.AgentInfo)]
	require.NotNil(t, participant)
	require.NotNil(t, participant.review)
	require.Equal(t, canaryReplacement.PodName, participant.podName)
	require.Equal(t, canaryReplacement.PodIp, participant.review.PodIp)
}

func TestServiceInterceptRemovalTransfersPublishedParticipantReview(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	stableSibling := serviceAgent("example-service", "stable-pod-2", "10.0.0.3")
	stableSibling.ApiPort = 9901
	stableSibling.FtpPort = 9902
	stableSibling.SftpPort = 9903
	stableSibling.InterceptTargets[0].ContainerName = "replacement-app"
	stableSibling.Containers = map[string]*rpc.AgentInfo_ContainerInfo{
		"replacement-app": {
			Environment: map[string]string{"POD": "replacement", "SECRET_NEW": "must-not-be-published"},
			MountPoint:  "/replacement/mount",
			Mounts:      map[string]int32{"/replacement/data": 1},
		},
	}
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	st.agents.Store("stable", stable)
	st.agents.Store("stable-sibling", stableSibling)
	st.agents.Store("canary", canary)

	spec := sharedServiceSpec()
	spec.ContainerName = "original-app"
	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:               "client:example-service",
		Spec:             spec,
		Disposition:      rpc.InterceptDispositionType_WAITING,
		ClientSession:    &rpc.SessionInfo{SessionId: "client"},
		ServiceWorkloads: sharedServiceWorkloads(stable.Name, canary.Name),
	}}
	st.initializeParticipants(intercept)
	st.intercepts.Store(intercept.Id, intercept)
	intercept = st.ApplyAgentReview(ctx, intercept.Id, stable, &rpc.ReviewInterceptRequest{
		Disposition:       rpc.InterceptDispositionType_ACTIVE,
		PodIp:             stable.PodIp,
		FtpPort:           8001,
		SftpPort:          8002,
		MountPoint:        "/old/mount",
		Mounts:            map[string]int32{"/old/data": 2},
		Environment:       map[string]string{"POD": "old"},
		MechanismArgsDesc: "header-filter",
	})
	intercept = st.ApplyAgentReview(ctx, intercept.Id, canary, &rpc.ReviewInterceptRequest{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       canary.PodIp,
	})
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)

	st.agents.Delete("stable")
	st.consolidateAgentSessionIntercepts(stable)

	intercept, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)
	require.Equal(t, stableSibling.PodName, intercept.PodName)
	require.Equal(t, stableSibling.PodIp, intercept.PodIp)
	require.Equal(t, stableSibling.ApiPort, intercept.ApiPort)
	require.Equal(t, stableSibling.FtpPort, intercept.FtpPort)
	require.Equal(t, stableSibling.SftpPort, intercept.SftpPort)
	require.Equal(t, "/replacement/mount", intercept.MountPoint)
	require.Equal(t, map[string]int32{"/replacement/data": 1}, intercept.Mounts)
	require.Equal(t, map[string]string{"POD": "replacement"}, intercept.Environment)
	require.Equal(t, "header-filter", intercept.MechanismArgsDesc)
	participant := intercept.participants[agentParticipantKey(stable.AgentInfo)]
	require.Equal(t, stableSibling.PodName, participant.podName)
	require.Equal(t, stableSibling.PodIp, participant.review.PodIp)
	require.NotNil(t, intercept.participants[agentParticipantKey(canary.AgentInfo)].review)
}

func TestServiceInterceptRemovalSelectsEligibleReplacementDeterministically(t *testing.T) {
	tests := []struct {
		name       string
		ineligible func(context.Context, *AgentSession)
	}{
		{
			name: "omitted environment",
			ineligible: func(_ context.Context, agent *AgentSession) {
				agent.ContainerEnvironmentOmitted = true
			},
		},
		{
			name: "inactive pod",
			ineligible: func(ctx context.Context, agent *AgentSession) {
				mutator.GetMap(ctx).Inactivate(k8sTypes.UID(agent.PodUid))
			},
		},
		{
			name: "missing pod address",
			ineligible: func(_ context.Context, agent *AgentSession) {
				agent.PodIp = ""
			},
		},
		{
			name: "unknown target container",
			ineligible: func(_ context.Context, agent *AgentSession) {
				agent.InterceptTargets[0].ContainerName = "missing-app"
				agent.Containers = map[string]*rpc.AgentInfo_ContainerInfo{
					"other-app": {},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, st := newServiceInterceptState(t)
			stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
			canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
			intercept := activeServiceIntercept(t, ctx, st, stable, canary)
			ineligible := serviceAgent("example-service", "a-ineligible-pod", "10.0.0.3")
			tt.ineligible(ctx, ineligible)
			last := serviceAgent("example-service", "z-eligible-pod", "10.0.0.4")
			first := serviceAgent("example-service", "m-eligible-pod", "10.0.0.5")
			st.agents.Store("ineligible", ineligible)
			st.agents.Store("last", last)
			st.agents.Store("first", first)

			st.agents.Delete("stable")
			st.consolidateAgentSessionIntercepts(stable)

			updated, ok := st.intercepts.Load(intercept.Id)
			require.True(t, ok)
			require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
			require.Equal(t, first.PodName, updated.PodName)
			require.Equal(t, first.PodIp, updated.PodIp)
		})
	}
}

func TestServiceInterceptRemovalRequeuesWhenReplacementContainerUnknown(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	stableSibling := serviceAgent("example-service", "stable-pod-2", "10.0.0.3")
	stableSibling.Containers = map[string]*rpc.AgentInfo_ContainerInfo{
		"other-app": {Environment: map[string]string{"POD": "replacement"}},
	}
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)
	intercept.Spec.ContainerName = "original-app"
	st.agents.Store("stable-sibling", stableSibling)

	st.agents.Delete("stable")
	st.consolidateAgentSessionIntercepts(stable)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
	participant := updated.participants[agentParticipantKey(stable.AgentInfo)]
	require.Nil(t, participant.review)
	require.Empty(t, participant.podName)
}

func TestServiceInterceptRemovalKeepsNoAgentWhenNoReplacementExists(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)

	st.agents.Delete("stable")
	st.consolidateAgentSessionIntercepts(stable)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_NO_AGENT, updated.Disposition)
	participant := updated.participants[agentParticipantKey(stable.AgentInfo)]
	require.Nil(t, participant.review)
	require.Empty(t, participant.podName)
}

func TestServiceInterceptPrunesParticipantAfterTargetClaimDisappears(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	replacement := serviceAgent("example-service-canary", "canary-pod-2", "10.0.0.3")
	replacement.InterceptTargets = nil
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)

	// The replacement is already present when the old claiming pod leaves.
	st.agents.Store("canary-replacement", replacement)
	st.agents.Delete("canary")
	st.consolidateAgentSessionIntercepts(canary)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
	require.Len(t, updated.participants, 1)
	require.NotContains(t, updated.participants, agentParticipantKey(canary.AgentInfo))
	require.Len(t, updated.ServiceWorkloads, 1)
	require.Equal(t, "example-service", updated.ServiceWorkloads[0].WorkloadName)
	code, message := st.checkAgentsForIntercept(updated)
	require.Equal(t, rpc.InterceptDispositionType_UNSPECIFIED, code)
	require.Empty(t, message)
}

func TestServiceInterceptPrunesParticipantWhenNonClaimingReplacementArrives(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	replacement := serviceAgent("example-service-canary", "canary-pod-2", "10.0.0.3")
	replacement.InterceptTargets = nil
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)

	// If the claiming pod disappears first, the intercept briefly has no
	// agent for this participant. The later replacement proves that the
	// workload no longer claims the Service target and must unblock it.
	st.agents.Delete("canary")
	st.consolidateAgentSessionIntercepts(canary)
	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_NO_AGENT, updated.Disposition)

	_, err := st.RestoreAgent(ctx, "canary-replacement", replacement.AgentInfo, nil, time.Now())
	require.NoError(t, err)

	updated, ok = st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
	require.Len(t, updated.participants, 1)
	require.NotContains(t, updated.participants, agentParticipantKey(canary.AgentInfo))
	require.Len(t, updated.ServiceWorkloads, 1)
	require.Equal(t, "example-service", updated.ServiceWorkloads[0].WorkloadName)
	code, message := st.checkAgentsForIntercept(updated)
	require.Equal(t, rpc.InterceptDispositionType_UNSPECIFIED, code)
	require.Empty(t, message)
}

func TestServiceInterceptRequeuesWhenNonClaimingParticipantLeaves(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	stable := serviceAgent("example-service", "stable-pod", "10.0.0.1")
	canary := serviceAgent("example-service-canary", "canary-pod", "10.0.0.2")
	legacyCanary := serviceAgent("example-service-canary", "canary-pod-2", "10.0.0.3")
	legacyCanary.InterceptTargets = nil
	intercept := activeServiceIntercept(t, ctx, st, stable, canary)

	_, err := st.RestoreAgent(ctx, "legacy-canary", legacyCanary.AgentInfo, nil, time.Now())
	require.NoError(t, err)
	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_NO_AGENT, updated.Disposition)

	st.RemoveSession(ctx, "legacy-canary")

	updated, ok = st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_WAITING, updated.Disposition)
	require.Len(t, updated.participants, 2)
	require.NotNil(t, updated.participants[agentParticipantKey(stable.AgentInfo)].review)
	require.NotNil(t, updated.participants[agentParticipantKey(canary.AgentInfo)].review)
	code, message := st.checkAgentsForIntercept(updated)
	require.Equal(t, rpc.InterceptDispositionType_UNSPECIFIED, code)
	require.Empty(t, message)
}

func TestNonServiceSiblingRemovalDoesNotResetActiveIntercept(t *testing.T) {
	_, st := newServiceInterceptState(t)
	recorded := serviceAgent("demo", "recorded-pod", "10.0.0.1")
	sibling := serviceAgent("demo", "sibling-pod", "10.0.0.2")
	st.agents.Store("recorded", recorded)

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "client:demo",
		Spec:        &rpc.InterceptSpec{Agent: "demo", Namespace: "default", WorkloadKind: "Deployment", Mechanism: "tcp"},
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		PodIp:       recorded.PodIp,
		PodName:     recorded.PodName,
		ClientSession: &rpc.SessionInfo{
			SessionId: "client",
		},
	}}
	st.intercepts.Store(intercept.Id, intercept)

	st.consolidateAgentSessionIntercepts(sibling)

	intercept, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, intercept.Disposition)
	require.Equal(t, recorded.PodIp, intercept.PodIp)
	require.Equal(t, recorded.PodName, intercept.PodName)
}

func TestAgentMatchesServiceInterceptByClaim(t *testing.T) {
	spec := sharedServiceSpec()
	require.True(t, AgentMatchesIntercept(serviceAgent("example-service-canary", "pod", "10.0.0.2").AgentInfo, spec))

	agent := serviceAgent("example-service-canary", "pod", "10.0.0.2").AgentInfo
	agent.InterceptTargets[0].ServiceUid = "other-service"
	require.False(t, AgentMatchesIntercept(agent, spec))

	legacyPrimary := serviceAgent("example-service", "pod", "10.0.0.1").AgentInfo
	legacyPrimary.InterceptTargets = nil
	require.True(t, AgentMatchesIntercept(legacyPrimary, spec))

	legacyCanary := serviceAgent("example-service-canary", "pod", "10.0.0.2").AgentInfo
	legacyCanary.InterceptTargets = nil
	require.False(t, AgentMatchesIntercept(legacyCanary, spec))
}

func TestServiceScopesOverlap(t *testing.T) {
	spec := sharedServiceSpec()
	same := sharedServiceSpec()
	same.Agent = "example-service-canary"
	require.True(t, serviceScopesOverlap(spec, same))

	otherPort := sharedServiceSpec()
	otherPort.ServicePort = 81
	require.False(t, serviceScopesOverlap(spec, otherPort))

	otherUID := sharedServiceSpec()
	otherUID.ServiceUid = "other-service"
	require.False(t, serviceScopesOverlap(spec, otherUID))
}

func TestServicePodIsRelevant(t *testing.T) {
	pod := &core.Pod{}
	require.True(t, servicePodIsRelevant(pod))

	pod.Status.Conditions = []core.PodCondition{{
		Type:   core.PodReady,
		Status: core.ConditionFalse,
	}}
	require.True(t, servicePodIsRelevant(pod))

	now := meta.Now()
	pod.DeletionTimestamp = &now
	require.False(t, servicePodIsRelevant(pod))
}

func TestUnsupportedSharedServiceModeFallsBackToRequestedWorkload(t *testing.T) {
	ctx, st := newServiceInterceptState(t)
	spec := sharedServiceSpec()
	spec.Mechanism = "tcp"

	st.ensureServiceWorkloads(ctx, spec, nil, nil, nil, agentconfig.ReplacePolicyIntercept, nil)

	require.False(t, serviceScopedIntercept(spec))
	require.Equal(t, "shared-service-uid", spec.ServiceUid)
	require.Equal(t, int32(80), spec.ServicePort)
	require.Equal(t, "example-service", spec.ServiceName)
}

func newSharedServiceFixture(t *testing.T, mutate ...func(stable, canary *apps.Deployment)) (context.Context, *State, *apps.Deployment, *apps.Deployment) {
	t.Helper()
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	for _, m := range mutate {
		m(stable, canary)
	}
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:              "ambassador",
		EnabledWorkloadKinds:          k8sapi.Kinds{k8sapi.DeploymentKind},
		InterceptInactiveBlockTimeout: time.Hour,
	})
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx
	return ctx, st, stable, canary
}

func denyWorkload(name string) participantAuthorizer {
	return func(_ context.Context, _ *auth.Principal, _, workloadName string) error {
		if workloadName == name {
			return status.Error(codes.PermissionDenied, "denied")
		}
		return nil
	}
}

func requireSingleWorkloadFallback(t *testing.T, ctx context.Context, spec *rpc.InterceptSpec, canary *apps.Deployment) {
	t.Helper()
	require.False(t, serviceScopedIntercept(spec))
	require.Equal(t, "shared-service-uid", spec.ServiceUid)
	require.Equal(t, int32(80), spec.ServicePort)
	require.Nil(t, mutator.GetMap(ctx).Get(canary.Name, canary.Namespace))
}

func ensureSharedServiceForAlice(ctx context.Context, t *testing.T, st *State, spec *rpc.InterceptSpec, stable *apps.Deployment) []*rpc.InterceptWorkload {
	t.Helper()
	primaryConfig, err := st.getOrCreateAgentConfig(ctx, k8sapi.Deployment(stable), false, true, spec, agentconfig.ReplacePolicyIntercept)
	require.NoError(t, err)
	client := newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now())
	st.clients.Store("client", client)
	return st.ensureServiceWorkloads(ctx, spec, k8sapi.Deployment(stable), primaryConfig, nil, agentconfig.ReplacePolicyIntercept, client)
}

func TestServiceInterceptFallsBackWhenSecondaryIsNotAuthorized(t *testing.T) {
	ctx, st, stable, canary := newSharedServiceFixture(t)
	st.SetParticipantAuthorizer(denyWorkload(canary.Name))
	spec := sharedServiceSpec()

	workloads := ensureSharedServiceForAlice(ctx, t, st, spec, stable)

	require.Nil(t, workloads)
	requireSingleWorkloadFallback(t, ctx, spec, canary)
}

func TestServiceInterceptFallsBackWhenSecondaryHasNodeAgentClaim(t *testing.T) {
	ctx, st, stable, canary := newSharedServiceFixture(t)
	st.leases = xsync.NewMap[leaseKey, struct{}]()
	st.clients.Store("client", newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now()))
	st.addLease("client", canary.Name, "default")
	spec := sharedServiceSpec()

	workloads := ensureSharedServiceForAlice(ctx, t, st, spec, stable)

	require.Nil(t, workloads)
	requireSingleWorkloadFallback(t, ctx, spec, canary)
}

func TestServiceInterceptWatcherFallsBackWhenLateWorkloadForbidsInjection(t *testing.T) {
	ctx, st, stable, canary := newSharedServiceFixture(t, func(_, canary *apps.Deployment) {
		canary.Spec.Template.Annotations = map[string]string{annotation.InjectTrafficAgent: "disabled"}
	})
	st.agents.Store("stable", serviceAgent("example-service", "stable-pod", "10.0.0.1"))
	st.clients.Store("client", newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now()))

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", stable.Name): {namespace: "default", kind: "Deployment", name: stable.Name},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.NotContains(t, updated.participants, participantKey("default", "Deployment", canary.Name))
	require.Equal(t, rpc.InterceptDispositionType_ACTIVE, updated.Disposition)
	requireSingleWorkloadFallback(t, ctx, updated.Spec, canary)
}

func TestServiceInterceptWatcherFallsBackWhenLateWorkloadIsNotAuthorized(t *testing.T) {
	ctx, st, stable, canary := newSharedServiceFixture(t)
	st.SetParticipantAuthorizer(denyWorkload(canary.Name))
	st.agents.Store("stable", serviceAgent("example-service", "stable-pod", "10.0.0.1"))
	st.clients.Store("client", newClientSessionState(ctx, "client", &rpc.ClientInfo{Name: "alice"}, time.Now()))

	intercept := &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:            "client:example-service",
		Spec:          sharedServiceSpec(),
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "client"},
	}}
	intercept.participants = map[string]*interceptParticipant{
		participantKey("default", "Deployment", stable.Name): {namespace: "default", kind: "Deployment", name: stable.Name},
	}
	intercept.syncServiceWorkloads()
	st.intercepts.Store(intercept.Id, intercept)

	st.reconcileServiceInterceptWorkloads(ctx, intercept.Id)

	updated, ok := st.intercepts.Load(intercept.Id)
	require.True(t, ok)
	require.NotContains(t, updated.participants, participantKey("default", "Deployment", canary.Name))
	requireSingleWorkloadFallback(t, ctx, updated.Spec, canary)
}

func TestServiceInterceptSecondaryIgnoresPrimaryContainerName(t *testing.T) {
	stable := serviceDeployment("example-service", map[string]string{"app": "example-service", "track": "stable"})
	canary := serviceDeployment("example-service-canary", map[string]string{"app": "example-service", "track": "canary"})
	canary.Spec.Template.Spec.Containers = []core.Container{
		{Name: "app", Ports: []core.ContainerPort{{ContainerPort: 9090}}},
		{Name: "backend", Ports: []core.ContainerPort{{ContainerPort: 8080}}},
	}
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Name:      "example-service",
			Namespace: "default",
			UID:       k8sTypes.UID("shared-service-uid"),
		},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "example-service"},
			Ports: []core.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}
	ctx := serviceDiscoveryContext(t, svc, stable, canary)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:              "ambassador",
		EnabledWorkloadKinds:          k8sapi.Kinds{k8sapi.DeploymentKind},
		InterceptInactiveBlockTimeout: time.Hour,
	})
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	_, st := newServiceInterceptState(t)
	st.backgroundCtx = ctx

	spec := sharedServiceSpec()
	spec.ContainerName = "app"
	primaryConfig, err := st.getOrCreateAgentConfig(ctx, k8sapi.Deployment(stable), false, true, spec, agentconfig.ReplacePolicyIntercept)
	require.NoError(t, err)
	canaryConfig, err := st.getOrCreateAgentConfig(ctx, k8sapi.Deployment(canary), false, false, nil, agentconfig.ReplacePolicyInactive)
	require.NoError(t, err)
	require.Len(t, canaryConfig.Containers, 2)
	for _, c := range canaryConfig.Containers {
		switch c.Name {
		case "app":
			c.Replace = agentconfig.ReplacePolicyContainer
		case "backend":
			c.Replace = agentconfig.ReplacePolicyInactive
		}
	}

	client := newClientSessionState(ctx, "alice", &rpc.ClientInfo{Name: "alice"}, time.Now())
	// The canary has no ready replicas, so this covers the getOrCreateAgentConfig branch.
	workloads := st.ensureServiceWorkloads(ctx, spec, k8sapi.Deployment(stable), primaryConfig, nil, agentconfig.ReplacePolicyIntercept, client)

	require.Len(t, workloads, 2)
	require.True(t, serviceScopedIntercept(spec))
	config := mutator.GetMap(ctx).Get(canary.Name, canary.Namespace)
	require.NotNil(t, config)
	replace := map[string]agentconfig.ReplacePolicy{}
	for _, c := range config.Containers {
		replace[c.Name] = c.Replace
	}
	require.Equal(t, agentconfig.ReplacePolicyContainer, replace["app"])
	require.Equal(t, agentconfig.ReplacePolicyIntercept, replace["backend"])
}
