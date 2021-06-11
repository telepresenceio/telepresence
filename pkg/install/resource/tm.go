package resource

import (
	"context"

	"github.com/datawire/ambassador/pkg/kates"
	cl "github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const telName = "manager"

func GetTrafficManagerResources() Instances {
	return Instances{
		TrafficManagerNamespaceKeep, // Don't delete when deleting the manager
		TrafficManagerServiceAccount,
		TrafficManagerClusterRole,
		TrafficManagerClusterRoleBinding,
		MutatorWebhookSecret,
		NewTrafficManagerDeployment(),
		TrafficManagerSvc,
		AgentInjectorSvc,
		AgentInjectorWebhook,
	}
}

func EnsureTrafficManager(ctx context.Context, client *kates.Client, namespace, clusterID string, env *cl.Env) error {
	ctx = withScope(ctx, &scope{
		namespace: namespace,
		clusterID: clusterID,
		tmSelector: map[string]string{
			"app":          install.ManagerAppName,
			"telepresence": telName,
		},
		client: client,
		env:    env,
	})
	return GetTrafficManagerResources().Ensure(ctx)
}

func DeleteTrafficManager(ctx context.Context, client *kates.Client, namespace string, env *cl.Env) error {
	ctx = withScope(ctx, &scope{
		namespace: namespace,
		client:    client,
		env:       env,
	})
	return GetTrafficManagerResources().Delete(ctx)
}
