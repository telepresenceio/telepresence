package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakeDiscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func fakeClientSet(t *testing.T, objects ...runtime.Object) kubernetes.Interface {
	cs := fake.NewClientset(objects...)
	dsc, ok := cs.Discovery().(*fakeDiscovery.FakeDiscovery)
	require.True(t, ok)
	dsc.FakedServerVersion = &version.Info{GitVersion: "v1.33.1"}
	return cs
}

func TestNewInfo_GetInstallID(t *testing.T) {
	env := managerutil.Env{
		ManagerNamespace: "test",
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

		cs := fakeClientSet(t, append(namespaces,
			&v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
					UID:  types.UID(defaultUID),
				},
			})...,
		)
		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info, err := NewInfo(ctx)
		require.NoError(t, err)
		require.NotNil(t, info)
		// always use manager ns to gen ID
		require.Equal(t, info.ID(), testUID)
	})

	t.Run("from non-default namespace", func(t *testing.T) {
		ctx := context.Background()

		cs := fakeClientSet(t, namespaces...)

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info, err := NewInfo(ctx)
		require.NoError(t, err)
		require.NotNil(t, info)
		require.Equal(t, info.ID(), testUID)
	})
}
