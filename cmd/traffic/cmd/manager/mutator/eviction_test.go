package mutator

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestEvictPod(t *testing.T) {
	tests := []struct {
		name      string
		evictErr  error
		expectErr string
		budgetErr bool
	}{
		{
			name: "evicted",
		},
		{
			name:     "pod already gone",
			evictErr: k8sErrors.NewNotFound(core.Resource("pods"), "echo-1"),
		},
		{
			name:      "disruption budget",
			evictErr:  k8sErrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0),
			expectErr: "disruption budget",
			budgetErr: true,
		},
		{
			name:      "other error",
			evictErr:  k8sErrors.NewBadRequest("nope"),
			expectErr: "nope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t, false)
			ci := fake.NewClientset()
			ci.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
				if a.GetSubresource() != "eviction" {
					return false, nil, nil
				}
				return true, nil, tt.evictErr
			})
			ctx = k8sapi.WithJoinedClientSetInterface(ctx, ci, argorolloutsfake.NewSimpleClientset())
			ctx = informer.WithFactory(ctx, "")
			err := evictPod(ctx, &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "echo-1", Namespace: "ns"}})
			if tt.expectErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.expectErr)
				require.Equal(t, tt.budgetErr, errors.As(err, &disruptionBudgetError{}))
			}
		})
	}
}
