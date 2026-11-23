package mutator

import (
	"context"
	"testing"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const untrustedNs, untrustedName = "some-ns", "echo"

// untrustedInjectorContext returns a context whose informer stores hold a Deployment (with the
// given pod template annotations), a Service selecting it, and the given pods.
func untrustedInjectorContext(t *testing.T, tplAnnotations map[string]string, pods ...*core.Pod) context.Context {
	one := int32(1)
	dep := &apps.Deployment{
		TypeMeta:   meta.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: meta.ObjectMeta{Name: untrustedName, Namespace: untrustedNs, UID: "dep-uid"},
		Spec: apps.DeploymentSpec{
			Replicas: &one,
			Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": untrustedName}},
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: map[string]string{"app": untrustedName}, Annotations: tplAnnotations},
				Spec: core.PodSpec{Containers: []core.Container{{
					Name:  "app",
					Image: "app-image",
					Ports: []core.ContainerPort{{Name: "http", ContainerPort: 8080}},
				}}},
			},
		},
	}
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: untrustedName, Namespace: untrustedNs, UID: "svc-uid"},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": untrustedName},
			Ports:    []core.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
		},
	}
	client := fake.NewSimpleClientset()
	ctx := testutil.NewContext(t, false)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		ManagerNamespace:     "default",
		AgentRegistry:        "ghcr.io/telepresenceio",
		AgentImageName:       "tel2",
		AgentImageTag:        "2.14.0",
		AgentPort:            9900,
		EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind},
	})
	ctx, err := managerutil.WithAgentImageRetriever(ctx, func(context.Context, string) error { return nil })
	require.NoError(t, err)
	f := informer.GetK8sFactory(ctx, "")
	require.NoError(t, f.Apps().V1().Deployments().Informer().GetStore().Add(dep))
	require.NoError(t, f.Core().V1().Services().Informer().GetStore().Add(svc))
	for _, p := range pods {
		require.NoError(t, f.Core().V1().Pods().Informer().GetStore().Add(p))
	}
	return ctx
}

func untrustedPod(uid string, anns map[string]string) *core.Pod {
	return &core.Pod{
		TypeMeta: meta.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: meta.ObjectMeta{
			Name:            "echo-abc",
			Namespace:       untrustedNs,
			UID:             types.UID(uid),
			Labels:          map[string]string{"app": untrustedName},
			Annotations:     anns,
			OwnerReferences: []meta.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: untrustedName, Controller: boolP(true)}},
		},
		Spec: core.PodSpec{Containers: []core.Container{{
			Name:  "app",
			Image: "app-image",
			Ports: []core.ContainerPort{{Name: "http", ContainerPort: 8080}},
		}}},
	}
}

func TestInject_ForgedCreateStoresNothing(t *testing.T) {
	ctx := untrustedInjectorContext(t, nil)
	cw := NewWatcher()
	a := agentInjector{agentConfigs: cw}
	pod := untrustedPod("p1", map[string]string{annotation.InjectTrafficAgent: "enabled"})
	_, err := a.Inject(ctx, toAdmissionRequest(podResource, pod))
	require.NoError(t, err)
	assert.Nil(t, cw.Get(untrustedName, untrustedNs), "a config must not be stored for a workload that did not ask for an agent")
}

func TestInject_CreateStoresWhenTemplateAsksForAgent(t *testing.T) {
	ctx := untrustedInjectorContext(t, map[string]string{annotation.InjectTrafficAgent: "enabled"})
	cw := NewWatcher()
	a := agentInjector{agentConfigs: cw}
	pod := untrustedPod("p1", map[string]string{annotation.InjectTrafficAgent: "enabled"})
	_, err := a.Inject(ctx, toAdmissionRequest(podResource, pod))
	require.NoError(t, err)
	assert.NotNil(t, cw.Get(untrustedName, untrustedNs))
}

func TestInject_UsesStoredConfigWithoutTemplateAnnotation(t *testing.T) {
	ctx := untrustedInjectorContext(t, nil)
	cw := NewWatcher()
	gc, err := managerutil.GetEnv(ctx).GeneratorConfig(managerutil.GetAgentImage(ctx))
	require.NoError(t, err)
	wl, err := agentmap.GetWorkload(ctx, untrustedName, untrustedNs, k8sapi.DeploymentKind)
	require.NoError(t, err)
	sc, err := gc.Generate(ctx, wl, nil)
	require.NoError(t, err)
	cw.Store(sc)

	a := agentInjector{agentConfigs: cw}
	pod := untrustedPod("p1", map[string]string{annotation.InjectTrafficAgent: "enabled"})
	patch, err := a.Inject(ctx, toAdmissionRequest(podResource, pod))
	require.NoError(t, err)
	assert.NotEmpty(t, patch, "a client-requested agent, stored before the pod exists, is still injected")
}

func deleteRequest(pod *core.Pod) *admission.AdmissionRequest {
	req := toAdmissionRequest(podResource, pod)
	req.Operation = admission.Delete
	req.OldObject = req.Object
	req.Object = runtime.RawExtension{}
	return req
}

func TestInject_DeleteReviewDoesNotInactivate(t *testing.T) {
	known := untrustedPod("known-uid", nil)
	ctx := untrustedInjectorContext(t, nil, known)
	cw := NewWatcher()
	a := agentInjector{agentConfigs: cw}

	_, err := a.Inject(ctx, deleteRequest(known))
	require.NoError(t, err)
	assert.False(t, cw.IsInactive("known-uid"))

	cw.(*configWatcher).inactivateTerminating(known, false)
	assert.False(t, cw.IsInactive("known-uid"), "a pod that isn't terminating stays active")
	now := meta.Now()
	known.DeletionTimestamp = &now
	cw.(*configWatcher).inactivateTerminating(known, false)
	assert.True(t, cw.IsInactive("known-uid"))
}
