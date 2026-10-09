package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func TestPodOrdinal(t *testing.T) {
	ordinal, err := podOrdinal("checkout-splitter-12")
	require.NoError(t, err)
	require.Equal(t, 12, ordinal)
	_, err = podOrdinal("checkout")
	require.Error(t, err)
}

func TestReadInitialRoutingTable(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	want := kafkaintercept.RoutingTable{Generation: 7, Paused: true}
	data, err := json.Marshal(want)
	require.NoError(t, err)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "split", Namespace: "ambassador"},
		Data:       map[string]string{runtimeconfig.RoutingDataKey: string(data)},
	}
	control := splitterControl{
		client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build(),
		config: runtimeconfig.Control{Namespace: "ambassador", ConfigMap: "split"},
	}

	got, err := control.readRoutingTable(t.Context())
	require.NoError(t, err)
	require.Equal(t, want, got)

	control.config.ConfigMap = "missing"
	_, err = control.readRoutingTable(t.Context())
	require.Error(t, err)
}
