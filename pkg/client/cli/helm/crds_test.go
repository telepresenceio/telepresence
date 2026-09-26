package helm

import (
	"context"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	fakeclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCRDName(t *testing.T) {
	name, err := crdName([]byte(`{"metadata":{"name":"splits.kafka.telepresence.io"}}`))
	require.NoError(t, err)
	assert.Equal(t, "splits.kafka.telepresence.io", name)

	_, err = crdName([]byte(`{"metadata":{}}`))
	assert.Error(t, err)

	_, err = crdName([]byte(`not json`))
	assert.Error(t, err)
}

// TestApplyCRDObjects applies the embedded chart's CRD objects (the Kafka
// CRDs) against a fake apiextensions clientset and asserts that both land,
// exercising the YAML-to-JSON decode and name-extraction path without a real
// *rest.Config.
func TestApplyCRDObjects(t *testing.T) {
	chrt, err := LoadCoreChart(semver.MustParse("2.31.0"))
	require.NoError(t, err)
	require.NotEmpty(t, chrt.CRDObjects(), "expected the chart to carry at least one CRD")

	client := fakeclientset.NewClientset()
	require.NoError(t, applyCRDObjects(context.Background(), client.ApiextensionsV1(), chrt))

	list, err := client.ApiextensionsV1().CustomResourceDefinitions().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	names := make(map[string]bool, len(list.Items))
	for _, item := range list.Items {
		names[item.Name] = true
	}
	assert.True(t, names["routes.kafka.telepresence.io"])
	assert.True(t, names["splits.kafka.telepresence.io"])
}
