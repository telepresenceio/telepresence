// Package runtimeconfig defines the provider-owned splitter configuration.
package runtimeconfig

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

const (
	// ProviderName identifies the provider's namespaced Kubernetes resources.
	ProviderName = "tp-kafka"
	// SplitterName identifies the provider's per-split data plane.
	SplitterName = "tp-kafka-splitter"
	// ConfigDataKey is the immutable splitter configuration entry.
	ConfigDataKey = "config.json"
	// RoutingDataKey is the independently updated routing table entry.
	RoutingDataKey = "routing.json"
	// SplitLabel associates provider resources with a KafkaSplit.
	SplitLabel = "kafka.telepresence.io/split"
	// ActiveAnnotation records active split generations on application Pods.
	ActiveAnnotation = "kafka.telepresence.io/active"
	// ConfigAnnotation restarts splitter Pods when immutable config changes.
	ConfigAnnotation = "kafka.telepresence.io/config"
	// GenerationAnnotation records the routing generation accepted by a splitter.
	GenerationAnnotation = "kafka.telepresence.io/gen"
	// HealthyAnnotation records splitter health on its member Lease.
	HealthyAnnotation = "kafka.telepresence.io/ok"
	// HandoffGate blocks replacement Pods during source handoff.
	HandoffGate = "kafka.telepresence.io/handoff"
	// SplitFinalizer protects split-owned Kafka resources during deletion.
	SplitFinalizer = "kafka.telepresence.io/split"
	// RouteFinalizer protects route-owned Kafka resources during deletion.
	RouteFinalizer = "kafka.telepresence.io/route"
)

// Control tells a splitter where routing generations and member acks live.
type Control struct {
	Namespace         string `json:"namespace"`
	ConfigMap         string `json:"configMap"`
	MemberLeasePrefix string `json:"memberLeasePrefix"`
	Split             string `json:"split"`
}

// Config is the complete startup configuration for one splitter StatefulSet.
type Config struct {
	Namespace             string                      `json:"namespace"`
	Connection            api.KafkaConnectionSpec     `json:"connection"`
	Group                 string                      `json:"group"`
	InstanceIDPrefix      string                      `json:"instanceIDPrefix"`
	TransactionalIDPrefix string                      `json:"transactionalIDPrefix"`
	AppTopics             map[string]string           `json:"appTopics"`
	OffsetReset           string                      `json:"offsetReset"`
	BatchSize             int                         `json:"batchSize"`
	Routes                kafkaintercept.RoutingTable `json:"routes"`
	Control               *Control                    `json:"control,omitempty"`
}

// RoutingFromConfigMap decodes the routing table stored in a splitter ConfigMap.
func RoutingFromConfigMap(configMap *corev1.ConfigMap) (kafkaintercept.RoutingTable, error) {
	var table kafkaintercept.RoutingTable
	if err := json.Unmarshal([]byte(configMap.Data[RoutingDataKey]), &table); err != nil {
		return table, fmt.Errorf("decode routing table from ConfigMap %s/%s: %w", configMap.Namespace, configMap.Name, err)
	}
	return table, nil
}
