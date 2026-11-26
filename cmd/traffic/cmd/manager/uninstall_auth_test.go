package manager

import (
	"context"
	"testing"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func uninstallAuthContext(t *testing.T, hookAccount string) context.Context {
	client := fake.NewSimpleClientset()
	k8sapi.InstallFakeTokenReviews(client, func(token string, _ []string) *authnv1.TokenReviewStatus {
		users := map[string]string{
			"hook":       "system:serviceaccount:ns:hook-sa",
			"other":      "system:serviceaccount:ns:other-sa",
			"other-ns":   "system:serviceaccount:elsewhere:hook-sa",
			"lookalike":  "system:serviceaccount:ns:hook-sa-2",
			"user-token": "alice",
		}
		if u, ok := users[token]; ok {
			return &authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: u}}
		}
		return &authnv1.TokenReviewStatus{Authenticated: false}
	})
	ctx := k8sapi.WithJoinedClientSetInterface(context.Background(), client, argorolloutsfake.NewSimpleClientset())
	return managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: "ns", UninstallHookServiceAccount: hookAccount})
}

func TestUninstallAuthenticator(t *testing.T) {
	authn := uninstallAuthenticator(uninstallAuthContext(t, "hook-sa"))
	require.NoError(t, authn(context.Background(), "hook"))
	for _, token := range []string{"other", "other-ns", "lookalike", "user-token", "unknown", ""} {
		assert.Error(t, authn(context.Background(), token), token)
	}
}

func TestUninstallAuthenticator_NoAccountConfigured(t *testing.T) {
	authn := uninstallAuthenticator(uninstallAuthContext(t, ""))
	assert.Error(t, authn(context.Background(), "hook"))
}
