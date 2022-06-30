package cluster

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestNewInfo_GetClusterID(t *testing.T) {
	env := managerutil.Env{
		ManagedNamespaces: "ambassador test",
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
		require.Equal(t, info.GetClusterID(), defaultUID)
	})

	t.Run("from non-default namespace", func(t *testing.T) {
		ctx := context.Background()

		cs := fake.NewSimpleClientset(namespaces...)

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info := NewInfo(ctx)
		require.NotNil(t, info)
		require.Equal(t, info.GetClusterID(), testUID)
	})

	t.Run("fail no license", func(t *testing.T) {
		ctx := context.Background()

		cs := fake.NewSimpleClientset(&v1.Pod{})

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		info := NewInfo(ctx)
		require.NotNil(t, info)
		require.Equal(t, info.GetClusterID(), license.ClusterIDZero)
	})

	t.Run("bad license jwt", func(t *testing.T) {
		ctx := context.Background()

		cs := fake.NewSimpleClientset(&v1.Pod{})

		ctx = k8sapi.WithK8sInterface(ctx, cs)
		ctx = managerutil.WithEnv(ctx, &env)

		tmpRootDir, err := os.MkdirTemp("", "")
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = os.RemoveAll(tmpRootDir) }()
		err = os.WriteFile(filepath.Join(tmpRootDir, "license"), []byte("INVALID"), os.ModePerm)
		require.NoError(t, err)
		err = os.WriteFile(filepath.Join(tmpRootDir, "hostDomain"), []byte("auth.datawire.io"), os.ModePerm)
		require.NoError(t, err)

		ctx = license.WithBundle(ctx, tmpRootDir)

		info := NewInfo(ctx)
		require.NotNil(t, info)
		require.Equal(t, info.GetClusterID(), license.ClusterIDZero)
	})
}
