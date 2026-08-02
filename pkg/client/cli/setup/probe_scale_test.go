package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestProbeNamespaceScale_Count(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ambassador"}},
	)
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNamespaceScale(context.Background())
	assert.Equal(t, 3, facts.Count)
	assert.False(t, facts.ListDenied)
}

func TestProbeNamespaceScale_Denied(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", nil)
	})
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNamespaceScale(context.Background())
	assert.Equal(t, 0, facts.Count)
	assert.True(t, facts.ListDenied)
}
