package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admission "k8s.io/api/admissionregistration/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func kafkaWebhookConfigurations(namespace string) []runtime.Object {
	name := kafkaWebhookConfigurationPrefix + namespace
	return []runtime.Object{
		&admission.MutatingWebhookConfiguration{ObjectMeta: meta.ObjectMeta{Name: name}},
		&admission.ValidatingWebhookConfiguration{ObjectMeta: meta.ObjectMeta{Name: name}},
	}
}

func kafkaValuesEnabled() *helm.Values {
	return &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
		Kafka:         helm.Kafka{Enabled: new(true)},
	}
}

func TestVerifyInstall_Kafka(t *testing.T) {
	t.Run("everything present and ready", func(t *testing.T) {
		objs := append([]runtime.Object{kafkaDeployment(1, 1)}, kafkaWebhookConfigurations("ambassador")...)
		client := fake.NewClientset(objs...)
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)

		notes := VerifyInstall(context.Background(), client, "ambassador", kafkaValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 3)
		for _, n := range notes {
			assert.Equal(t, NoteInfo, n.Level)
		}
		assert.Equal(t, "the KafkaSplit and KafkaRoute CRDs are served", notes[0].Text)
		assert.Contains(t, notes[1].Text, "the tp-kafka deployment has 1 of 1 replicas ready")
		assert.Equal(t, "the tp-kafka webhook configurations are present", notes[2].Text)
	})

	t.Run("deployment unready", func(t *testing.T) {
		objs := append([]runtime.Object{kafkaDeployment(1, 0)}, kafkaWebhookConfigurations("ambassador")...)
		client := fake.NewClientset(objs...)
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)

		notes := VerifyInstall(context.Background(), client, "ambassador", kafkaValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 3)
		assert.Equal(t, NoteWarning, notes[1].Level)
		assert.Contains(t, notes[1].Text, "0 of 1 replicas ready")
	})

	t.Run("a webhook configuration is missing", func(t *testing.T) {
		client := fake.NewClientset(
			kafkaDeployment(1, 1),
			&admission.MutatingWebhookConfiguration{ObjectMeta: meta.ObjectMeta{Name: kafkaWebhookConfigurationPrefix + "ambassador"}},
		)
		withGroupResources(client, kafkaGroupVersion, kafkaSplitsResource, kafkaRoutesResource)

		notes := VerifyInstall(context.Background(), client, "ambassador", kafkaValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 3)
		assert.Equal(t, NoteWarning, notes[2].Level)
		assert.Contains(t, notes[2].Text, "ValidatingWebhookConfiguration")
		assert.Contains(t, notes[2].Text, "was not found")
	})

	t.Run("CRDs not served", func(t *testing.T) {
		objs := append([]runtime.Object{kafkaDeployment(1, 1)}, kafkaWebhookConfigurations("ambassador")...)
		client := fake.NewClientset(objs...)

		notes := VerifyInstall(context.Background(), client, "ambassador", kafkaValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 3)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "not served")
	})

	t.Run("Kafka disabled adds no notes", func(t *testing.T) {
		client := fake.NewClientset()
		notes := VerifyInstall(context.Background(), client, "ambassador", &helm.Values{
			AgentInjector: helm.AgentInjector{Enabled: new(false)},
			Kafka:         helm.Kafka{Enabled: new(false)},
		}, ClientAuthFacts{})
		assert.Empty(t, notes)
	})
}
