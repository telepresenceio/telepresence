package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func withGroupResources(client *fake.Clientset, groupVersion string, resources ...string) {
	fd := client.Discovery().(*discoveryfake.FakeDiscovery)
	apiResources := make([]meta.APIResource, 0, len(resources))
	for _, r := range resources {
		apiResources = append(apiResources, meta.APIResource{Name: r, Namespaced: true})
	}
	fd.Resources = append(fd.Resources, &meta.APIResourceList{GroupVersion: groupVersion, APIResources: apiResources})
}

func splitObject(name, phase string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kafkaGroupVersion,
		"kind":       "KafkaSplit",
		"metadata":   map[string]any{"name": name},
		"status":     map[string]any{"phase": phase},
	}}
}

func TestKafkaCRDsFinding(t *testing.T) {
	t.Run("served", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)
		assert.Equal(t, VerdictYes, kafkaCRDsFinding(context.Background(), client).Verdict)
	})
	t.Run("absent", func(t *testing.T) {
		client := fake.NewClientset()
		f := kafkaCRDsFinding(context.Background(), client)
		assert.Equal(t, VerdictNo, f.Verdict)
		assert.Contains(t, f.Evidence[0], "is not served")
	})
	t.Run("group served but a resource missing", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource)
		f := kafkaCRDsFinding(context.Background(), client)
		assert.Equal(t, VerdictNo, f.Verdict)
		assert.Contains(t, f.Evidence[0], kafkaRoutesResource)
	})
	t.Run("discovery error", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("transport failure")
		})
		f := kafkaCRDsFinding(context.Background(), client)
		assert.Equal(t, VerdictUnknown, f.Verdict)
	})
}

func TestArgoRolloutsFinding(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, argoGroupVersion, argoRolloutsResource)
		assert.Equal(t, VerdictYes, argoRolloutsFinding(context.Background(), client).Verdict)
	})
	t.Run("absent", func(t *testing.T) {
		client := fake.NewClientset()
		assert.Equal(t, VerdictNo, argoRolloutsFinding(context.Background(), client).Verdict)
	})
}

func TestProbeKafka_ActiveSplits(t *testing.T) {
	t.Run("counts active, excludes disabled", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)
		p := &Prober{
			KubeClient: client,
			KafkaSplits: func(context.Context) ([]unstructured.Unstructured, error) {
				return []unstructured.Unstructured{
					splitObject("a", "Enabled"),
					splitObject("b", "Starting"),
					splitObject("c", "Disabled"),
				}, nil
			},
		}
		facts := p.probeKafka(context.Background())
		assert.Equal(t, VerdictYes, facts.CRDs.Verdict)
		assert.Equal(t, 2, facts.ActiveSplits)
		assert.False(t, facts.SplitsListDenied)
		assert.Empty(t, facts.SplitsListError)
	})
	t.Run("forbidden list sets SplitsListDenied", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)
		p := &Prober{
			KubeClient: client,
			KafkaSplits: func(context.Context) ([]unstructured.Unstructured, error) {
				return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "splits"}, "", nil)
			},
		}
		facts := p.probeKafka(context.Background())
		assert.True(t, facts.SplitsListDenied)
		assert.Empty(t, facts.SplitsListError)
		assert.Equal(t, 0, facts.ActiveSplits)
	})
	t.Run("other list error sets SplitsListError", func(t *testing.T) {
		client := fake.NewClientset()
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)
		p := &Prober{
			KubeClient: client,
			KafkaSplits: func(context.Context) ([]unstructured.Unstructured, error) {
				return nil, errors.New("transport failure")
			},
		}
		facts := p.probeKafka(context.Background())
		assert.False(t, facts.SplitsListDenied)
		assert.NotEmpty(t, facts.SplitsListError)
	})
	t.Run("CRDs absent skips the splits list entirely", func(t *testing.T) {
		client := fake.NewClientset()
		called := false
		p := &Prober{
			KubeClient: client,
			KafkaSplits: func(context.Context) ([]unstructured.Unstructured, error) {
				called = true
				return nil, nil
			},
		}
		facts := p.probeKafka(context.Background())
		assert.Equal(t, VerdictNo, facts.CRDs.Verdict)
		assert.False(t, called)
		assert.Equal(t, 0, facts.ActiveSplits)
	})
}

func TestDefaultKafkaSplits_NoRESTClient(t *testing.T) {
	client := fake.NewClientset()
	_, err := defaultKafkaSplits(context.Background(), client)
	require.Error(t, err)
}
