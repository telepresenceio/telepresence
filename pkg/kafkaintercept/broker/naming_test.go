package broker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceNames(t *testing.T) {
	object := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "checkout",
	}}
	names := ResourceNames(object, "checkout-group")
	require.Equal(t, Names{
		SourceGroup:           "checkout-group",
		ApplicationGroup:      "checkout-group.tp-app",
		SplitterInstance:      "tp-splitter-",
		SplitterTransactional: "checkout-group.tp-splitter-",
		KubernetesName:        "shop-checkout",
	}, names)
	require.Equal(t, "orders.tp.checkout-group.app", names.ApplicationTopic("orders"))
	require.Equal(t, "checkout-group.tp.alice-checkout", names.SessionGroup("alice-checkout"))
	require.Equal(t, "orders.tp.checkout-group.alice-checkout", names.SessionTopic("alice-checkout", "orders"))
	require.Equal(t, "checkout-group.tp-app", names.ApplicationTransactional())
	require.Equal(t, "checkout-group.tp-client.alice-checkout", names.ClientTransactional("alice-checkout"))
	require.Equal(t, "checkout-group.tp-drain.alice-checkout", names.DrainTransactional("alice-checkout"))
}

func TestGeneratedNameValidation(t *testing.T) {
	tests := map[string]struct {
		object    metav1.Object
		group     string
		topic     string
		route     string
		validate  func(Names, string, string) error
		wantError string
	}{
		"valid application": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
			group:  "checkout", topic: "orders",
			validate: func(names Names, topic, _ string) error {
				return names.ValidateApplication([]string{topic}, 2, true, true)
			},
		},
		"long application topic": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
			group:  "checkout", topic: strings.Repeat("a", maxKafkaTopicLength),
			validate: func(names Names, topic, _ string) error {
				return names.ValidateApplication([]string{topic}, 1, true, false)
			},
			wantError: "application topic",
		},
		"long Kubernetes name": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: strings.Repeat("n", 40), Name: strings.Repeat("s", 30)}},
			group:  "checkout", topic: "orders",
			validate: func(names Names, topic, _ string) error {
				return names.ValidateApplication([]string{topic}, 1, true, false)
			},
			wantError: "shorten the namespace or KafkaSplit name",
		},
		"group is not topic safe": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
			group:  "checkout/group", topic: "orders",
			validate: func(names Names, topic, _ string) error {
				return names.ValidateApplication([]string{topic}, 1, true, false)
			},
			wantError: "use a source group containing only",
		},
		"preprovisioned group need not be topic safe": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
			group:  "checkout/group", topic: "orders",
			validate: func(names Names, topic, _ string) error {
				return names.ValidateApplication([]string{topic}, 1, false, false)
			},
		},
		"long session group": {
			object: &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"}},
			group:  strings.Repeat("g", maxKafkaIDLength), topic: "orders", route: "alice-checkout",
			validate: func(names Names, topic, route string) error {
				return names.ValidateSession(route, []string{topic}, true, false)
			},
			wantError: "session group",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.validate(ResourceNames(test.object, test.group), test.topic, test.route)
			if test.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantError)
			}
		})
	}
}
