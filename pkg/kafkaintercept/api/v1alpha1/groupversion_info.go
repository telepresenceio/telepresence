// Package v1alpha1 contains the Kubernetes API for Kafka personal intercepts.
// +kubebuilder:object:generate=true
// +groupName=kafka.telepresence.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion identifies the initial Kafka intercept API.
	GroupVersion = schema.GroupVersion{Group: "kafka.telepresence.io", Version: "v1alpha1"}

	// SchemeBuilder registers Kafka intercept API objects.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds Kafka intercept API objects to a runtime scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
