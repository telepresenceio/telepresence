package mutator

import (
	"context"
	"testing"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestUpdatedServiceIncludesNewlySelectedWorkload(t *testing.T) {
	const namespace = "default"
	stable := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "example-service", Namespace: namespace},
		Spec: apps.DeploymentSpec{
			Template: core.PodTemplateSpec{ObjectMeta: meta.ObjectMeta{Labels: map[string]string{
				"app":   "example-service",
				"track": "stable",
			}}},
		},
	}
	canary := stable.DeepCopy()
	canary.Name = "example-service-canary"
	canary.Spec.Template.Labels["track"] = "canary"
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "example-service", Namespace: namespace, UID: types.UID("service-uid")},
		// Model a selector update that stopped selecting stable and now selects canary.
		// The old stable claimant still needs regeneration so that it drops the Service.
		Spec: core.ServiceSpec{Selector: map[string]string{"track": "canary"}},
	}

	client := fake.NewSimpleClientset(stable, canary)
	ctx := k8sapi.WithJoinedClientSetInterface(context.Background(), client, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	deploymentStore := informer.GetK8sFactory(ctx, namespace).Apps().V1().Deployments().Informer().GetStore()
	for _, deployment := range []*apps.Deployment{stable, canary} {
		require.NoError(t, deploymentStore.Add(deployment.DeepCopy()))
	}

	cw := NewWatcher().(*configWatcher)
	cw.Store(&agentconfig.Sidecar{
		AgentName:    stable.Name,
		Namespace:    namespace,
		WorkloadName: stable.Name,
		WorkloadKind: k8sapi.DeploymentKind,
		Containers: []*agentconfig.Container{{
			Intercepts: []*agentconfig.Intercept{{ServiceUID: svc.UID}},
		}},
	})
	cw.Store(&agentconfig.Sidecar{
		AgentName:    canary.Name,
		Namespace:    namespace,
		WorkloadName: canary.Name,
		WorkloadKind: k8sapi.DeploymentKind,
	})

	affected := cw.configsAffectedBySvc(ctx, svc, true)
	names := make([]string, 0, len(affected))
	for _, config := range affected {
		names = append(names, config.sc.AgentName)
	}
	assert.ElementsMatch(t, []string{stable.Name, canary.Name}, names)

	affected = cw.configsAffectedBySvc(ctx, svc, false)
	names = names[:0]
	for _, config := range affected {
		names = append(names, config.sc.AgentName)
	}
	assert.Equal(t, []string{stable.Name}, names)

	svc.Spec.Selector = nil
	affected = cw.configsAffectedBySvc(ctx, svc, true)
	names = names[:0]
	for _, config := range affected {
		names = append(names, config.sc.AgentName)
	}
	assert.Equal(t, []string{stable.Name}, names)
}
