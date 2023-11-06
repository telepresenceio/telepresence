package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

func TestNewInfo_GetClusterID(t *testing.T) {
	env := managerutil.Env{
		ManagedNamespaces: []string{"ambassador test"},
		ManagerNamespace:  "test",
	}

	testUID := "test-uid"
	defaultUID := "default-uid"
	namespaces := []runtime.Object{
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ambassador",
				UID:  "ambassador_uid",
			},
		},
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
				UID:  types.UID(testUID),
			},
		},
	}

	t.Run("from default namespace", func(t *testing.T) {
		ctx := context.Background()

		cs := fake.NewSimpleClientset(append(namespaces,
			&v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
					UID:  types.UID(defaultUID),
				},
			})...,
		)

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info := NewInfo(ctx)
		require.NotNil(t, info)
		// always use manager ns to gen ID
		require.Equal(t, info.ID(), testUID)
	})

	t.Run("from non-default namespace", func(t *testing.T) {
		ctx := context.Background()

		cs := fake.NewSimpleClientset(namespaces...)

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info := NewInfo(ctx)
		require.NotNil(t, info)
		require.Equal(t, info.ID(), testUID)
	})
}
