package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

// fakeRoutingApplier records every RoutingTable ApplyRoutingTable receives.
type fakeRoutingApplier struct {
	applied chan kafkaintercept.RoutingTable
}

func (f *fakeRoutingApplier) ApplyRoutingTable(table kafkaintercept.RoutingTable) error {
	f.applied <- table
	return nil
}

func routingConfigMap(namespace, name string, generation uint64) *corev1.ConfigMap {
	data, err := json.Marshal(kafkaintercept.RoutingTable{Generation: generation})
	if err != nil {
		panic(err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       map[string]string{runtimeconfig.RoutingDataKey: string(data)},
	}
}

func TestControlRunWatchesRoutingConfigMap(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no
	// bookmark events), which is enabled by default in client-go v0.35+.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	const namespace, name = "ambassador", "split"
	configMap := routingConfigMap(namespace, name, 1)
	clientset := fake.NewClientset(configMap)

	control := &splitterControl{
		clientset:    clientset,
		config:       runtimeconfig.Control{Namespace: namespace, ConfigMap: name},
		acknowledged: &atomic.Uint64{},
	}
	applier := &fakeRoutingApplier{applied: make(chan kafkaintercept.RoutingTable, 4)}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- control.run(ctx, applier) }()

	select {
	case table := <-applier.applied:
		require.Equal(t, uint64(1), table.Generation)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the initial routing table")
	}
	// Give the informer's initial List+Watch time to settle before mutating
	// the ConfigMap, or the fake clientset's reflector falls into a relist
	// backoff that delays the update event by several seconds.
	time.Sleep(100 * time.Millisecond)

	updated := routingConfigMap(namespace, name, 2)
	updated.ResourceVersion = configMap.ResourceVersion
	_, err := clientset.CoreV1().ConfigMaps(namespace).Update(ctx, updated, metav1.UpdateOptions{})
	require.NoError(t, err)

	select {
	case table := <-applier.applied:
		require.Equal(t, uint64(2), table.Generation)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the updated routing table")
	}

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for control.run to return")
	}
}
