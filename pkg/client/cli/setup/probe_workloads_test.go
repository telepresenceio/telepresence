package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func deployment(name, ns string, port int32) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "app"}},
				},
			},
		},
	}
	if port > 0 {
		d.Spec.Template.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: port}}
	}
	return d
}

func TestProbeWorkloads(t *testing.T) {
	t.Run("samples with and without ports, chart workloads excluded", func(t *testing.T) {
		client := fake.NewClientset(
			deployment("echo-easy", "default", 8080),
			deployment("no-ports", "default", 0),
			deployment("traffic-manager", "default", 8081),
			deployment("quic-forwarder", "default", 7778),
		)
		p := &Prober{KubeClient: client, WorkloadNamespace: "default"}
		facts := p.probeWorkloads(context.Background())
		require.Len(t, facts.Samples, 2)
		assert.Equal(t, WorkloadSample{Name: "echo-easy", Namespace: "default", Port: 8080}, facts.Samples[0])
		assert.Equal(t, WorkloadSample{Name: "no-ports", Namespace: "default"}, facts.Samples[1])
	})
	t.Run("at most three samples", func(t *testing.T) {
		client := fake.NewClientset(
			deployment("a", "default", 80),
			deployment("b", "default", 80),
			deployment("c", "default", 80),
			deployment("d", "default", 80),
		)
		p := &Prober{KubeClient: client, WorkloadNamespace: "default"}
		assert.Len(t, p.probeWorkloads(context.Background()).Samples, 3)
	})
	t.Run("denial leaves the facts empty", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
		})
		p := &Prober{KubeClient: client, WorkloadNamespace: "default"}
		assert.Empty(t, p.probeWorkloads(context.Background()).Samples)
	})
	t.Run("no namespace skips the probe", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset()}
		assert.Empty(t, p.probeWorkloads(context.Background()).Samples)
	})
}
