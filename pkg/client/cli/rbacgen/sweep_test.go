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

func TestSweep_DeniedSorted(t *testing.T) {
	client, sent := recordingClient(func(ra *auth.ResourceAttributes) bool { return ra.Resource != "clusterroles" })
	ras := []*auth.ResourceAttributes{
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "b"},
		{Verb: "create", Resource: "secrets", Namespace: "ns"},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "a"},
	}
	denied, err := Sweep(context.Background(), client, ras)
	require.NoError(t, err)
	assert.Len(t, sent(), 3)
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
