package rbacgen

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auth "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// recordingClient answers reviews with allow and records what was sent.
func recordingClient(allow func(*auth.ResourceAttributes) bool) (*fake.Clientset, func() []auth.ResourceAttributes) {
	var mu sync.Mutex
	var sent []auth.ResourceAttributes
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		mu.Lock()
		sent = append(sent, *ra)
		mu.Unlock()
		return allow(ra)
	})
	return client, func() []auth.ResourceAttributes {
		mu.Lock()
		defer mu.Unlock()
		return sent
	}
}

func TestSweep_CreateSentWithoutName(t *testing.T) {
	// Models a rule with resourceNames, which matches only a named review.
	client, sent := recordingClient(func(ra *auth.ResourceAttributes) bool {
		return ra.Name == "allowed"
	})
	ras := []*auth.ResourceAttributes{
		{Verb: "create", Resource: "configmaps", Namespace: "ns", Name: "allowed"},
		{Verb: "get", Resource: "configmaps", Namespace: "ns", Name: "allowed"},
	}
	denied, err := Sweep(context.Background(), client, ras)
	require.NoError(t, err)

	require.Len(t, denied, 1)
	assert.Equal(t, "create", denied[0].Verb)
	assert.Equal(t, "allowed", denied[0].Name, "the name is kept for reporting")
	assert.ElementsMatch(t, []auth.ResourceAttributes{
		{Verb: "create", Resource: "configmaps", Namespace: "ns"},
		{Verb: "get", Resource: "configmaps", Namespace: "ns", Name: "allowed"},
	}, sent())
}

func TestSweep_SharedReviewReportsEveryAttribute(t *testing.T) {
	client, sent := recordingClient(func(*auth.ResourceAttributes) bool { return false })
	ras := []*auth.ResourceAttributes{
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "b"},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "a"},
	}
	denied, err := Sweep(context.Background(), client, ras)
	require.NoError(t, err)
	assert.Len(t, sent(), 1)
	require.Len(t, denied, 2)
	assert.Equal(t, "a", denied[0].Name)
	assert.Equal(t, "b", denied[1].Name)
}

func TestSweep_ReviewCallError(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server down")
	})
	_, err := Sweep(context.Background(), client, []*auth.ResourceAttributes{{Verb: "create", Resource: "secrets"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api server down")
}

func TestAllowed_CreateSentWithoutName(t *testing.T) {
	client, sent := recordingClient(func(*auth.ResourceAttributes) bool { return true })
	ra := &auth.ResourceAttributes{Verb: "create", Resource: "rolebindings", Namespace: "kube-system", Name: "x"}
	allowed, err := Allowed(context.Background(), client, ra)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Equal(t, []auth.ResourceAttributes{{Verb: "create", Resource: "rolebindings", Namespace: "kube-system"}}, sent())
	assert.Equal(t, "x", ra.Name, "the attributes passed in are not changed")
}
