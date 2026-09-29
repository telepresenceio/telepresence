package golden

import (
	"io"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestKafkaWebhookSelectsProviderNamespaceWithSplit(t *testing.T) {
	out := renderChart(t, map[string]any{"kafka": map[string]any{"enabled": true}})
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out[kafkaTpl]), 4096)
	for {
		config := new(admissionv1.MutatingWebhookConfiguration)
		err := decoder.Decode(config)
		if err == io.EOF {
			t.Fatal("Kafka Pod webhook configuration was not rendered")
		}
		if err != nil {
			t.Fatalf("decode Kafka chart: %v", err)
		}
		if config.Kind != "MutatingWebhookConfiguration" {
			continue
		}
		for _, webhook := range config.Webhooks {
			if webhook.Name != "pods.kafka.telepresence.io" {
				continue
			}
			namespaceSelector, err := metav1.LabelSelectorAsSelector(webhook.NamespaceSelector)
			if err != nil {
				t.Fatalf("Pod webhook namespace selector: %v", err)
			}
			providerNamespace := labels.Set{
				"kubernetes.io/metadata.name":  releaseNamespace,
				"kafka.telepresence.io/splits": "true",
			}
			if !namespaceSelector.Matches(providerNamespace) {
				t.Fatal("Pod webhook excludes a provider namespace containing a KafkaSplit")
			}
			delete(providerNamespace, "kafka.telepresence.io/splits")
			if namespaceSelector.Matches(providerNamespace) {
				t.Fatal("Pod webhook selects a namespace without a KafkaSplit")
			}
			objectSelector, err := metav1.LabelSelectorAsSelector(webhook.ObjectSelector)
			if err != nil {
				t.Fatalf("Pod webhook object selector: %v", err)
			}
			if objectSelector.Matches(labels.Set{"app.kubernetes.io/name": "tp-kafka"}) {
				t.Fatal("Pod webhook selects its own provider Pod")
			}
			if !objectSelector.Matches(labels.Set{"app.kubernetes.io/name": "event-statistics"}) {
				t.Fatal("Pod webhook excludes an application Pod")
			}
			return
		}
	}
}
