package setup

import (
	"context"
	"strings"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// kafkaWebhookConfigurationPrefix combined with the manager namespace names
// the chart's Kafka MutatingWebhookConfiguration and
// ValidatingWebhookConfiguration (both share the same name).
const kafkaWebhookConfigurationPrefix = "tp-kafka-"

// verifyKafka performs the Kafka provider's post-apply checks: the CRDs are
// served, the tp-kafka Deployment is ready, and its webhook configurations
// are present.
func verifyKafka(ctx context.Context, ki kubernetes.Interface, namespace string) []Note {
	var notes []Note

	crds := kafkaCRDsFinding(ctx, ki)
	if crds.Verdict == VerdictYes {
		notes = append(notes, Note{Level: NoteInfo, Text: "the KafkaSplit and KafkaRoute CRDs are served"})
	} else {
		notes = append(notes, noteFromFinding(crds))
	}

	provider := kafkaProviderFinding(ctx, ki, namespace)
	if provider.Verdict == VerdictYes {
		notes = append(notes, Note{Level: NoteInfo, Text: "the " + kafkaDeploymentName + " deployment has " + strings.Join(provider.Evidence, "; ")})
	} else {
		notes = append(notes, noteFromFinding(provider))
	}

	notes = append(notes, verifyKafkaWebhooks(ctx, ki, namespace)...)
	return notes
}

// verifyKafkaWebhooks checks that the Kafka provider's mutating and
// validating webhook configurations are present, naming whichever is
// missing.
func verifyKafkaWebhooks(ctx context.Context, ki kubernetes.Interface, namespace string) []Note {
	name := kafkaWebhookConfigurationPrefix + namespace
	var missing []string
	if _, err := ki.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, name, meta.GetOptions{}); err != nil {
		missing = append(missing, "MutatingWebhookConfiguration "+name)
	}
	if _, err := ki.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, name, meta.GetOptions{}); err != nil {
		missing = append(missing, "ValidatingWebhookConfiguration "+name)
	}
	if len(missing) == 0 {
		return []Note{{Level: NoteInfo, Text: "the tp-kafka webhook configurations are present"}}
	}
	notes := make([]Note, len(missing))
	for i, m := range missing {
		notes[i] = Note{Level: NoteWarning, Text: "the " + m + " was not found"}
	}
	return notes
}
