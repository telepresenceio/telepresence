package golden

import (
	"bufio"
	"io"
	"maps"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"
)

func quicForwarderResource(t *testing.T, output map[string]string, template, kind, name string) *unstructured.Unstructured {
	t.Helper()

	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(output[template]), 4096)
	for {
		resource := &unstructured.Unstructured{}
		if err := decoder.Decode(resource); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode %s: %v", template, err)
		}
		if resource.GetKind() == kind && resource.GetName() == name {
			return resource
		}
	}

	t.Fatalf("%s does not contain %s %q", template, kind, name)
	return nil
}

func quicForwarderStringMap(t *testing.T, resource *unstructured.Unstructured, fields ...string) map[string]string {
	t.Helper()

	values, found, err := unstructured.NestedStringMap(resource.Object, fields...)
	if err != nil {
		t.Fatalf("read %s %q %s: %v", resource.GetKind(), resource.GetName(), strings.Join(fields, "."), err)
	}
	if !found {
		t.Fatalf("%s %q has no %s", resource.GetKind(), resource.GetName(), strings.Join(fields, "."))
	}
	return values
}

func quicForwarderStrictYAML(t *testing.T, template, manifest string) {
	t.Helper()

	reader := yaml.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	for {
		document, err := reader.Read()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("read %s: %v", template, err)
		}
		if _, err = sigyaml.YAMLToJSONStrict(document); err != nil {
			t.Errorf("%s contains invalid or duplicate YAML keys: %v", template, err)
		}
	}
}

func TestQuicForwarderPodLabels(t *testing.T) {
	const (
		labelKey   = "example.com/mesh-injection"
		labelValue = "enabled"
		serviceTpl = "telepresence-oss/templates/service.yaml"
	)

	expectedSelector := map[string]string{
		"app":          "quic-forwarder",
		"telepresence": "quic-forwarder",
	}

	t.Run("custom labels affect only the forwarder pod", func(t *testing.T) {
		output := renderChart(t, map[string]any{
			"quicTunnel": map[string]any{
				"enabled": true,
				"forwarder": map[string]any{
					"podLabels": map[string]any{labelKey: labelValue},
				},
			},
		})

		forwarder := quicForwarderResource(t, output, quicFwdTpl, "Deployment", "quic-forwarder")
		podLabels := quicForwarderStringMap(t, forwarder, "spec", "template", "metadata", "labels")
		expectedPodLabels := maps.Clone(expectedSelector)
		expectedPodLabels[labelKey] = labelValue
		if !maps.Equal(podLabels, expectedPodLabels) {
			t.Errorf("forwarder pod labels = %v, want %v", podLabels, expectedPodLabels)
		}

		deploymentSelector := quicForwarderStringMap(t, forwarder, "spec", "selector", "matchLabels")
		if !maps.Equal(deploymentSelector, expectedSelector) {
			t.Errorf("forwarder deployment selector = %v, want %v", deploymentSelector, expectedSelector)
		}
		if _, exists := quicForwarderStringMap(t, forwarder, "metadata", "labels")[labelKey]; exists {
			t.Errorf("custom pod label unexpectedly appears on forwarder deployment metadata")
		}

		manager := quicForwarderResource(t, output, statefulsetTpl, "StatefulSet", "traffic-manager")
		if _, exists := quicForwarderStringMap(t, manager, "spec", "template", "metadata", "labels")[labelKey]; exists {
			t.Errorf("custom forwarder pod label unexpectedly appears on manager pods")
		}

		service := quicForwarderResource(t, output, serviceTpl, "Service", "traffic-manager-quic")
		serviceSelector := quicForwarderStringMap(t, service, "spec", "selector")
		if !maps.Equal(serviceSelector, expectedSelector) {
			t.Errorf("forwarder service selector = %v, want %v", serviceSelector, expectedSelector)
		}

		for template, manifest := range output {
			if template != quicFwdTpl && strings.Contains(manifest, labelKey) {
				t.Errorf("custom forwarder pod label unexpectedly appears in %s", template)
			}
		}
		if count := strings.Count(output[quicFwdTpl], labelKey); count != 1 {
			t.Errorf("custom forwarder pod label appears %d times, want once", count)
		}
	})

	t.Run("selector labels take precedence over custom pod labels", func(t *testing.T) {
		const managerLabelKey = "example.com/manager-label"
		output := renderChart(t, map[string]any{
			"podLabels": map[string]any{
				"app":           "other-manager",
				"telepresence":  "other-manager",
				managerLabelKey: labelValue,
			},
			"quicTunnel": map[string]any{
				"enabled": true,
				"forwarder": map[string]any{
					"podLabels": map[string]any{
						"app":          "other-forwarder",
						"telepresence": "other-forwarder",
						labelKey:       labelValue,
					},
				},
			},
		})

		for _, tc := range []struct {
			name       string
			template   string
			kind       string
			selector   map[string]string
			label      string
			otherLabel string
		}{
			{
				name:       "quic-forwarder",
				template:   quicFwdTpl,
				kind:       "Deployment",
				selector:   expectedSelector,
				label:      labelKey,
				otherLabel: managerLabelKey,
			},
			{
				name:       "traffic-manager",
				template:   statefulsetTpl,
				kind:       "StatefulSet",
				selector:   map[string]string{"app": "traffic-manager", "telepresence": "manager"},
				label:      managerLabelKey,
				otherLabel: labelKey,
			},
		} {
			deployment := quicForwarderResource(t, output, tc.template, tc.kind, tc.name)
			selector := quicForwarderStringMap(t, deployment, "spec", "selector", "matchLabels")
			if !maps.Equal(selector, tc.selector) {
				t.Errorf("%s deployment selector = %v, want %v", tc.name, selector, tc.selector)
			}

			podLabels := quicForwarderStringMap(t, deployment, "spec", "template", "metadata", "labels")
			expectedPodLabels := maps.Clone(tc.selector)
			expectedPodLabels[tc.label] = labelValue
			if !maps.Equal(podLabels, expectedPodLabels) {
				t.Errorf("%s pod labels = %v, want %v", tc.name, podLabels, expectedPodLabels)
			}
			if _, exists := podLabels[tc.otherLabel]; exists {
				t.Errorf("%s pod unexpectedly has %q", tc.name, tc.otherLabel)
			}

			quicForwarderStrictYAML(t, tc.template, output[tc.template])
		}
	})

	t.Run("default labels preserve forwarder selectors", func(t *testing.T) {
		output := renderChart(t, map[string]any{
			"quicTunnel": map[string]any{"enabled": true},
		})
		forwarder := quicForwarderResource(t, output, quicFwdTpl, "Deployment", "quic-forwarder")
		podLabels := quicForwarderStringMap(t, forwarder, "spec", "template", "metadata", "labels")
		if !maps.Equal(podLabels, expectedSelector) {
			t.Errorf("default forwarder pod labels = %v, want %v", podLabels, expectedSelector)
		}
	})

	for _, tc := range []struct {
		name   string
		values map[string]any
	}{
		{name: "disabled by default"},
		{
			name: "explicitly disabled with custom labels",
			values: map[string]any{
				"quicTunnel": map[string]any{
					"enabled": false,
					"forwarder": map[string]any{
						"podLabels": map[string]any{labelKey: labelValue},
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := renderChart(t, tc.values)
			if rendered(output, quicFwdTpl) {
				t.Errorf("forwarder unexpectedly renders when the QUIC tunnel is disabled")
			}
			if !rendered(output, statefulsetTpl) {
				t.Errorf("manager statefulset does not render when the QUIC tunnel is disabled")
			}
			if strings.Contains(output[serviceTpl], "name: traffic-manager-quic") {
				t.Errorf("forwarder service unexpectedly renders when the QUIC tunnel is disabled")
			}
			for template, manifest := range output {
				if strings.Contains(manifest, labelKey) {
					t.Errorf("custom forwarder pod label unexpectedly appears in %s", template)
				}
			}
		})
	}
}
