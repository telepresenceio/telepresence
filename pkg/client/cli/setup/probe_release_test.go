package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func TestManagerWorkload_Kind(t *testing.T) {
	t.Run("statefulset present reports StatefulSet", func(t *testing.T) {
		one := int32(1)
		client := fake.NewClientset(&apps.StatefulSet{
			ObjectMeta: meta.ObjectMeta{Name: managerStatefulSetName, Namespace: "ambassador"},
			Spec:       apps.StatefulSetSpec{Replicas: &one},
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		assert.Equal(t, "StatefulSet", p.managerWorkload(context.Background()).Kind)
	})
	t.Run("only a deployment present reports Deployment", func(t *testing.T) {
		one := int32(1)
		client := fake.NewClientset(&apps.Deployment{
			ObjectMeta: meta.ObjectMeta{Name: managerStatefulSetName, Namespace: "ambassador"},
			Spec:       apps.DeploymentSpec{Replicas: &one},
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		assert.Equal(t, "Deployment", p.managerWorkload(context.Background()).Kind)
	})
	t.Run("neither readable is empty", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(), ManagerNamespace: "ambassador"}
		mw := p.managerWorkload(context.Background())
		assert.Empty(t, mw.Kind)
		assert.True(t, mw.NotFound)
	})
}

func TestReleaseFacts_ReadableValues(t *testing.T) {
	t.Run("not installed is nil", func(t *testing.T) {
		f := &ReleaseFacts{}
		assert.NoError(t, f.ReadableValues())
	})
	t.Run("installed with values read fine is nil", func(t *testing.T) {
		f := &ReleaseFacts{Installed: true, Values: &helm.Values{}}
		assert.NoError(t, f.ReadableValues())
	})
	t.Run("installed with a conversion failure reports the cause", func(t *testing.T) {
		f := &ReleaseFacts{Installed: true, ValuesError: `unknown values key "bogus"`}
		err := f.ReadableValues()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be read")
		assert.Contains(t, err.Error(), `unknown values key "bogus"`)
		assert.Contains(t, err.Error(), "cannot propose an upgrade")
	})
}
