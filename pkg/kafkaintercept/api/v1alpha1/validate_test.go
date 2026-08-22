package v1alpha1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validSplit() *KafkaSplit {
	return &KafkaSplit{Spec: KafkaSplitSpec{
		DesiredState:     DesiredStateEnabled,
		WorkloadSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
		Container:        "app",
		Connection:       KafkaConnectionSpec{BootstrapServers: []string{"kafka:9092"}},
		Source:           KafkaSourceSpec{Group: "checkout", Topics: []string{"orders"}, OffsetReset: "earliest"},
		Application: KafkaApplicationSpec{
			TopicEnv: "ORDERS_TOPIC", TopicSeparator: ",", GroupEnv: "KAFKA_GROUP",
			IsolationLevelEnv: "KAFKA_ISOLATION_LEVEL", TransactionalIDEnv: "KAFKA_TXN_ID",
		},
		Splitter: KafkaSplitterSpec{Replicas: 2, BatchSize: 100},
		Shadows:  KafkaShadowSpec{Mode: ShadowModeManaged, Managed: &KafkaManagedShadows{}},
	}}
}

func TestKafkaSplitValidation(t *testing.T) {
	require.NoError(t, validSplit().Validate())

	split := validSplit()
	split.Spec.Application.GroupEnv = split.Spec.Application.TopicEnv
	require.ErrorContains(t, split.Validate(), "used by both")

	split = validSplit()
	split.Spec.Shadows = KafkaShadowSpec{Mode: ShadowModePreprovisioned, Preprovisioned: &KafkaPreprovisionedShadows{}}
	require.ErrorContains(t, split.Validate(), "applicationGroup")
}

func TestPreprovisionedResourceValidation(t *testing.T) {
	preprovisioned := func() *KafkaSplit {
		split := validSplit()
		split.Spec.Source.Topics = []string{"orders", "payments"}
		split.Spec.Shadows = KafkaShadowSpec{Mode: ShadowModePreprovisioned, Preprovisioned: &KafkaPreprovisionedShadows{
			ApplicationGroup:  "checkout-app",
			ApplicationTopics: map[string]string{"orders": "orders-app", "payments": "payments-app"},
			Sessions: []KafkaSessionSlot{
				{Name: "alice", Group: "checkout-alice", Topics: map[string]string{"orders": "orders-alice", "payments": "payments-alice"}},
				{Name: "bob", Group: "checkout-bob", Topics: map[string]string{"orders": "orders-bob", "payments": "payments-bob"}},
			},
		}}
		return split
	}
	require.NoError(t, preprovisioned().Validate())

	tests := map[string]struct {
		mutate  func(*KafkaPreprovisionedShadows)
		message string
	}{
		"application topic aliases source": {
			func(pre *KafkaPreprovisionedShadows) { pre.ApplicationTopics["orders"] = "orders" },
			`preprovisioned topic "orders" is used by both source topic and application topic`,
		},
		"application topics alias": {
			func(pre *KafkaPreprovisionedShadows) { pre.ApplicationTopics["payments"] = "orders-app" },
			`preprovisioned topic "orders-app" is used by both application topic`,
		},
		"session topic aliases application": {
			func(pre *KafkaPreprovisionedShadows) { pre.Sessions[0].Topics["orders"] = "orders-app" },
			`preprovisioned topic "orders-app" is used by both application topic`,
		},
		"session topics alias": {
			func(pre *KafkaPreprovisionedShadows) { pre.Sessions[1].Topics["orders"] = "orders-alice" },
			`preprovisioned topic "orders-alice" is used by both session "alice"`,
		},
		"application group aliases source": {
			func(pre *KafkaPreprovisionedShadows) { pre.ApplicationGroup = "checkout" },
			`preprovisioned group "checkout" is used by both source group and application group`,
		},
		"session groups alias": {
			func(pre *KafkaPreprovisionedShadows) { pre.Sessions[1].Group = "checkout-alice" },
			`preprovisioned group "checkout-alice" is used by both session "alice" and session "bob"`,
		},
		"session names alias": {
			func(pre *KafkaPreprovisionedShadows) { pre.Sessions[1].Name = "alice" },
			`preprovisioned session name "alice" is used by both session slot 1 and session slot 2`,
		},
		"application topics contain extra source": {
			func(pre *KafkaPreprovisionedShadows) { pre.ApplicationTopics["paymnts"] = "paymnts-app" },
			`preprovisioned applicationTopics contains unknown source "paymnts"`,
		},
		"session topics contain extra source": {
			func(pre *KafkaPreprovisionedShadows) { pre.Sessions[0].Topics["paymnts"] = "paymnts-alice" },
			`preprovisioned session "alice" topics contains unknown source "paymnts"`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			split := preprovisioned()
			tt.mutate(split.Spec.Shadows.Preprovisioned)
			require.ErrorContains(t, split.Validate(), tt.message)
		})
	}
}

func TestKafkaAuthenticationValidation(t *testing.T) {
	split := validSplit()
	split.Spec.Connection.SASL = &KafkaSASLSpec{
		Mechanism: "GSSAPI",
		Kerberos: &KafkaKerberosSpec{
			ServiceName: "kafka", Realm: "EXAMPLE.COM", Username: ValueSource{Value: "alice"},
			Password: &ValueSource{Value: "password"},
			Keytab:   &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: "client.keytab"},
			Config:   &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: "krb5.conf"},
		},
	}
	require.ErrorContains(t, split.Validate(), "exactly one")

	split.Spec.Connection.SASL = &KafkaSASLSpec{
		Mechanism: "OAUTHBEARER",
		OAuth:     &KafkaOAuthSpec{ClientID: ValueSource{Value: "id"}, ClientSecret: ValueSource{Value: "secret"}},
	}
	require.ErrorContains(t, split.Validate(), "tokenURL")
}

func TestKafkaRouteValidation(t *testing.T) {
	route := &KafkaRoute{Spec: KafkaRouteSpec{
		SplitRef: struct {
			Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
		}{Name: "checkout"},
		AttachmentID: "attachment", SessionID: "session", ExpiresAt: metav1.NewTime(time.Now().Add(time.Minute)),
		DesiredState: RouteStateActive,
		Predicate:    KafkaRoutePredicate{Headers: []KafkaHeaderMatch{{Name: "tenant", Value: []byte("blue")}}},
	}}
	require.NoError(t, route.Validate())
	route.Spec.Predicate.Key = []byte("x")
	route.Spec.Predicate.KeyPrefix = []byte("x")
	require.Error(t, route.Validate())
}
