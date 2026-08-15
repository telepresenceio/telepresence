package queueconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/queueconfig"
)

// podTemplate builds a minimal PodTemplateSpec with the given app containers, plus any
// extra volumes.
func podTemplate(volumes []core.Volume, containers ...core.Container) *core.PodTemplateSpec {
	return &core.PodTemplateSpec{
		Spec: core.PodSpec{
			Containers: containers,
			Volumes:    volumes,
		},
	}
}

// literalEnv returns an EnvVar with a plain literal value.
func literalEnv(name, value string) core.EnvVar {
	return core.EnvVar{Name: name, Value: value}
}

// secretEnv returns an EnvVar sourced from a Secret key.
func secretEnv(name, secret, key string) core.EnvVar {
	return core.EnvVar{
		Name: name,
		ValueFrom: &core.EnvVarSource{
			SecretKeyRef: &core.SecretKeySelector{
				LocalObjectReference: core.LocalObjectReference{Name: secret},
				Key:                  key,
			},
		},
	}
}

// fieldRefEnv returns an EnvVar sourced from a pod field.
func fieldRefEnv(name, path string) core.EnvVar {
	return core.EnvVar{
		Name: name,
		ValueFrom: &core.EnvVarSource{
			FieldRef: &core.ObjectFieldSelector{FieldPath: path},
		},
	}
}

// kafkaQueue returns a fully-populated Kafka Queue whose env references are satisfied by
// appEnv, used as the happy-path base for validate tests.
func kafkaQueue(container string) queueconfig.Queue {
	return queueconfig.Queue{
		Name:      "orders",
		Container: container,
		Provider: &queueconfig.Kafka{
			BrokersEnv:  "KAFKA_BROKERS",
			SourceEnv:   "ORDERS_TOPIC",
			GroupEnv:    "KAFKA_GROUP",
			OffsetReset: queueconfig.OffsetResetEarliest,
		},
	}
}

func appEnv() []core.EnvVar {
	return []core.EnvVar{
		literalEnv("KAFKA_BROKERS", "kafka:9092"),
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
	}
}

// rabbitmqQueue returns a fully-populated RabbitMQ Queue, used as a second queue in
// env-collision tests alongside kafkaQueue.
func rabbitmqQueue(container string) queueconfig.Queue {
	return queueconfig.Queue{
		Name:      "invoices",
		Container: container,
		Provider: &queueconfig.RabbitMQ{
			URLEnv:           "AMQP_URL",
			ManagementURLEnv: "RABBITMQ_MANAGEMENT_URL",
			SourceEnv:        "INVOICE_QUEUE",
		},
	}
}

func TestValidateHappyPath(t *testing.T) {
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{kafkaQueue("")}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateHappyPathExcludesTrafficAgent(t *testing.T) {
	// The container is unspecified. With the traffic-agent container excluded, "app"
	// is the sole remaining candidate, so selection succeeds without ambiguity.
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{kafkaQueue("")}}
	tpl := podTemplate(nil,
		core.Container{Name: "app", Env: appEnv()},
		core.Container{Name: "traffic-agent"},
	)
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateContainerAmbiguous(t *testing.T) {
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{kafkaQueue("")}}
	tpl := podTemplate(nil,
		core.Container{Name: "app1", Env: appEnv()},
		core.Container{Name: "app2", Env: appEnv()},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "container is required")
}

func TestValidateContainerNotFound(t *testing.T) {
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{kafkaQueue("app")}}
	tpl := podTemplate(nil, core.Container{Name: "other", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, `container "app" not found`)
}

func TestValidateContainerExplicitTrafficAgentRejected(t *testing.T) {
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{kafkaQueue(agentconfig.ContainerName)}}
	tpl := podTemplate(nil,
		core.Container{Name: "app", Env: appEnv()},
		core.Container{Name: agentconfig.ContainerName},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, agentconfig.ContainerName)
	assert.ErrorContains(t, err, "not an app container")
}

func TestValidateSourceEnvViaValueFrom(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		secretEnv("ORDERS_TOPIC", "some-secret", "topic"),
		literalEnv("KAFKA_BROKERS", "kafka:9092"),
		literalEnv("KAFKA_GROUP", "orders-group"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka.sourceEnv")
	assert.ErrorContains(t, err, "must be a literal env value")
}

func TestValidateSourceEnvEmptyLiteral(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", ""),
		literalEnv("KAFKA_BROKERS", "kafka:9092"),
		literalEnv("KAFKA_GROUP", "orders-group"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "non-empty literal value")
}

func TestValidateGroupEnvNotPresent(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_BROKERS", "kafka:9092"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka.groupEnv")
	assert.ErrorContains(t, err, "is not an env var")
}

func TestValidateConnectionEnvViaEnvFromOnly(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{
		Name: "app",
		Env:  env,
		EnvFrom: []core.EnvFromSource{
			{ConfigMapRef: &core.ConfigMapEnvSource{LocalObjectReference: core.LocalObjectReference{Name: "app-config"}}},
		},
	})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka.brokersEnv")
	assert.ErrorContains(t, err, "envFrom is not supported")
}

func TestValidateConnectionEnvFieldRefRejected(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
		fieldRefEnv("KAFKA_BROKERS", "status.podIP"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "fieldRef")
}

func TestValidateConnectionEnvResourceFieldRefRejected(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
		{
			Name: "KAFKA_BROKERS",
			ValueFrom: &core.EnvVarSource{
				ResourceFieldRef: &core.ResourceFieldSelector{Resource: "limits.cpu"},
			},
		},
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "resourceFieldRef")
}

func TestValidateConnectionEnvConfigMapKeyRefAllowed(t *testing.T) {
	q := kafkaQueue("app")
	env := []core.EnvVar{
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
		{
			Name: "KAFKA_BROKERS",
			ValueFrom: &core.EnvVarSource{
				ConfigMapKeyRef: &core.ConfigMapKeySelector{
					LocalObjectReference: core.LocalObjectReference{Name: "kafka-config"},
					Key:                  "brokers",
				},
			},
		},
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateTLSSecretReferencedByEnv(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	env := append(appEnv(), secretEnv("_UNUSED", "kafka-ca", "ca.crt"))
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateTLSSecretReferencedByVolume(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name:         "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "kafka-ca"}},
		}},
		core.Container{
			Name:         "app",
			Env:          appEnv(),
			VolumeMounts: []core.VolumeMount{{Name: "ca-vol", MountPath: "/etc/kafka-ca"}},
		},
	)
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateTLSSecretReferencedByProjectedVolumeRejected(t *testing.T) {
	// Projected volumes are rejected in v1: the workload author must expose the CA key
	// directly on a plain Secret volume or env entry.
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name: "ca-vol",
			VolumeSource: core.VolumeSource{
				Projected: &core.ProjectedVolumeSource{
					Sources: []core.VolumeProjection{
						{Secret: &core.SecretProjection{LocalObjectReference: core.LocalObjectReference{Name: "kafka-ca"}}},
					},
				},
			},
		}},
		core.Container{
			Name:         "app",
			Env:          appEnv(),
			VolumeMounts: []core.VolumeMount{{Name: "ca-vol", MountPath: "/etc/kafka-ca"}},
		},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka.tls.caSecret")
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateTLSSecretReferencedByEnvWrongKeyRejected(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	env := append(appEnv(), secretEnv("_UNUSED", "kafka-ca", "other-key"))
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateTLSSecretVolumeItemsWrongKeyRejected(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name: "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{
				SecretName: "kafka-ca",
				Items:      []core.KeyToPath{{Key: "other-key", Path: "other-key"}},
			}},
		}},
		core.Container{
			Name:         "app",
			Env:          appEnv(),
			VolumeMounts: []core.VolumeMount{{Name: "ca-vol", MountPath: "/etc/kafka-ca"}},
		},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateTLSSecretVolumeItemsNamingCAKeyAllowed(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name: "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{
				SecretName: "kafka-ca",
				Items:      []core.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
			}},
		}},
		core.Container{
			Name:         "app",
			Env:          appEnv(),
			VolumeMounts: []core.VolumeMount{{Name: "ca-vol", MountPath: "/etc/kafka-ca"}},
		},
	)
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateTLSSecretVolumeSubPathOnlyRejected(t *testing.T) {
	// A SubPath mount exposes a single file, not proof that the CA key is available under
	// its own name, so it does not satisfy the reference requirement.
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name:         "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "kafka-ca"}},
		}},
		core.Container{
			Name: "app",
			Env:  appEnv(),
			VolumeMounts: []core.VolumeMount{
				{Name: "ca-vol", MountPath: "/etc/kafka-ca/ca.crt", SubPath: "ca.crt"},
			},
		},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateTLSSecretVolumeSubPathAndPlainMountAllowed(t *testing.T) {
	// A second, plain mount of the same volume (no SubPath/SubPathExpr) is proof the whole
	// Secret is available, even though the container also has a SubPath mount of it.
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name:         "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "kafka-ca"}},
		}},
		core.Container{
			Name: "app",
			Env:  appEnv(),
			VolumeMounts: []core.VolumeMount{
				{Name: "ca-vol", MountPath: "/etc/kafka-ca/ca.crt", SubPath: "ca.crt"},
				{Name: "ca-vol", MountPath: "/etc/kafka-ca-full"},
			},
		},
	)
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateTLSSecretNotReferenced(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka.tls.caSecret")
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateTLSSecretVolumeNotMounted(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).TLS = &queueconfig.TLS{CASecret: "kafka-ca", CAKey: "ca.crt"}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(
		[]core.Volume{{
			Name:         "ca-vol",
			VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "kafka-ca"}},
		}},
		// The volume exists but is not mounted by the selected container.
		core.Container{Name: "app", Env: appEnv()},
	)
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is not exposed by container")
}

func TestValidateRabbitMQHappyPath(t *testing.T) {
	q := queueconfig.Queue{
		Name:      "invoices",
		Container: "app",
		Provider: &queueconfig.RabbitMQ{
			URLEnv:           "AMQP_URL",
			ManagementURLEnv: "RABBITMQ_MANAGEMENT_URL",
			SourceEnv:        "INVOICE_QUEUE",
			TLS:              &queueconfig.TLS{CASecret: "rabbitmq-ca", CAKey: "ca.crt"},
		},
	}
	env := []core.EnvVar{
		literalEnv("AMQP_URL", "amqps://broker"),
		literalEnv("RABBITMQ_MANAGEMENT_URL", "https://broker:15672"),
		literalEnv("INVOICE_QUEUE", "invoices"),
		secretEnv("_UNUSED", "rabbitmq-ca", "ca.crt"),
	}
	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateEnvCollisionSameSourceEnv(t *testing.T) {
	q1 := kafkaQueue("app")
	q2 := kafkaQueue("app")
	q2.Name = "shipments"
	q2.Provider.(*queueconfig.Kafka).GroupEnv = "SHIPMENTS_GROUP"
	// q2 keeps q1's SourceEnv, "ORDERS_TOPIC".

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q1, q2}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`queue "orders": kafka.sourceEnv "ORDERS_TOPIC" collides with kafka.sourceEnv "ORDERS_TOPIC" of queue "shipments" in container "app"`)
}

func TestValidateEnvCollisionSourceEqualsGroupSameQueue(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).GroupEnv = "ORDERS_TOPIC"

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`queue "orders": kafka.sourceEnv "ORDERS_TOPIC" collides with kafka.groupEnv "ORDERS_TOPIC" of queue "orders" in container "app"`)
}

func TestValidateEnvCollisionOverrideCollidesWithOwnConnectionEnv(t *testing.T) {
	q := kafkaQueue("app")
	q.Provider.(*queueconfig.Kafka).SourceEnv = "KAFKA_BROKERS"

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`queue "orders": kafka.sourceEnv "KAFKA_BROKERS" collides with kafka.brokersEnv "KAFKA_BROKERS" of queue "orders" in container "app"`)
}

func TestValidateEnvCollisionOverrideCollidesWithOtherQueueConnectionEnv(t *testing.T) {
	q1 := kafkaQueue("app")
	q1.Provider.(*queueconfig.Kafka).SourceEnv = "SHARED"
	q2 := rabbitmqQueue("app")
	q2.Provider.(*queueconfig.RabbitMQ).URLEnv = "SHARED"

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q1, q2}}
	env := append(appEnv(),
		literalEnv("RABBITMQ_MANAGEMENT_URL", "https://broker:15672"),
		literalEnv("INVOICE_QUEUE", "invoices"))
	tpl := podTemplate(nil, core.Container{Name: "app", Env: env})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`queue "orders": kafka.sourceEnv "SHARED" collides with rabbitmq.urlEnv "SHARED" of queue "invoices" in container "app"`)
}

func TestValidateEnvCollisionAllowedAcrossContainers(t *testing.T) {
	// Both queues declare the same sourceEnv name, but resolve to different
	// containers, so no collision applies.
	q1 := kafkaQueue("app1")
	q2 := kafkaQueue("app2")
	q2.Name = "shipments"
	q2.Provider.(*queueconfig.Kafka).GroupEnv = "SHIPMENTS_GROUP"

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q1, q2}}
	tpl := podTemplate(nil,
		core.Container{Name: "app1", Env: appEnv()},
		core.Container{Name: "app2", Env: []core.EnvVar{
			literalEnv("KAFKA_BROKERS", "kafka:9092"),
			literalEnv("ORDERS_TOPIC", "orders"),
			literalEnv("SHIPMENTS_GROUP", "shipments-group"),
		}},
	)
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateEnvCollisionAllowedForSharedConnectionEnv(t *testing.T) {
	q1 := kafkaQueue("app")
	q2 := kafkaQueue("app")
	q2.Name = "shipments"
	q2.Provider.(*queueconfig.Kafka).SourceEnv = "SHIPMENTS_TOPIC"
	q2.Provider.(*queueconfig.Kafka).GroupEnv = "SHIPMENTS_GROUP"
	// Both queues share brokersEnv "KAFKA_BROKERS"; connection vars may repeat.

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q1, q2}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: []core.EnvVar{
		literalEnv("KAFKA_BROKERS", "kafka:9092"),
		literalEnv("ORDERS_TOPIC", "orders"),
		literalEnv("KAFKA_GROUP", "orders-group"),
		literalEnv("SHIPMENTS_TOPIC", "shipments"),
		literalEnv("SHIPMENTS_GROUP", "shipments-group"),
	}})
	assert.NoError(t, cfg.Validate(tpl))
}

func TestValidateMultipleQueuesAccumulateErrors(t *testing.T) {
	q1 := kafkaQueue("app")
	q1.Name = "orders"
	q2 := kafkaQueue("app")
	q2.Name = "shipments"
	q2.Provider.(*queueconfig.Kafka).SourceEnv = "MISSING_TOPIC"

	cfg := &queueconfig.Config{Queues: []queueconfig.Queue{q1, q2}}
	tpl := podTemplate(nil, core.Container{Name: "app", Env: appEnv()})
	err := cfg.Validate(tpl)
	require.Error(t, err)
	assert.ErrorContains(t, err, `queue "shipments"`)
	assert.ErrorContains(t, err, "MISSING_TOPIC")
}

func TestValidateCollisionsSameContainer(t *testing.T) {
	q1 := kafkaQueue("app")
	q2 := kafkaQueue("app")
	q2.Name = "shipments"
	q2.Provider.(*queueconfig.Kafka).GroupEnv = "SHIPMENTS_GROUP"
	// q2 keeps q1's SourceEnv, "ORDERS_TOPIC".

	err := queueconfig.ValidateCollisions([]*queueconfig.Queue{&q1, &q2})
	require.Error(t, err)
	assert.ErrorContains(t, err,
		`queue "orders": kafka.sourceEnv "ORDERS_TOPIC" collides with kafka.sourceEnv "ORDERS_TOPIC" of queue "shipments" in container "app"`)
}

func TestValidateCollisionsDifferentContainers(t *testing.T) {
	q1 := kafkaQueue("app1")
	q2 := kafkaQueue("app2")
	q2.Name = "shipments"
	// q2 keeps q1's SourceEnv, "ORDERS_TOPIC", but resolves to a different container.

	assert.NoError(t, queueconfig.ValidateCollisions([]*queueconfig.Queue{&q1, &q2}))
}

func TestValidateCollisionsRequiresContainer(t *testing.T) {
	q := kafkaQueue("")
	err := queueconfig.ValidateCollisions([]*queueconfig.Queue{&q})
	require.Error(t, err)
	assert.ErrorContains(t, err, "container is required")
}
