package queueconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/queueconfig"
)

const kafkaQueueYAML = `
queues:
  - name: orders
    container: app
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
      tls:
        caSecret: kafka-ca
        caKey: ca.crt
      sasl:
        mechanism: SCRAM-SHA-512
        usernameEnv: KAFKA_USERNAME
        passwordEnv: KAFKA_PASSWORD
`

const rabbitMQQueueYAML = `
queues:
  - name: invoices
    container: app
    rabbitmq:
      urlEnv: AMQP_URL
      managementUrlEnv: RABBITMQ_MANAGEMENT_URL
      sourceEnv: INVOICE_QUEUE
      tls:
        caSecret: rabbitmq-ca
        caKey: ca.crt
`

const mixedQueueYAML = kafkaQueueYAML + `
  - name: invoices
    container: app
    rabbitmq:
      urlEnv: AMQP_URL
      managementUrlEnv: RABBITMQ_MANAGEMENT_URL
      sourceEnv: INVOICE_QUEUE
`

func TestParseHappyPath(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"kafka queue", kafkaQueueYAML},
		{"rabbitmq queue", rabbitMQQueueYAML},
		{"mixed kafka and rabbitmq queues", mixedQueueYAML},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := queueconfig.Parse(tt.yaml)
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.NotEmpty(t, cfg.Queues)
		})
	}
}

func TestParseKafkaFields(t *testing.T) {
	cfg, err := queueconfig.Parse(kafkaQueueYAML)
	require.NoError(t, err)
	require.Len(t, cfg.Queues, 1)

	q := cfg.Queues[0]
	assert.Equal(t, "orders", q.Name)
	assert.Equal(t, "app", q.Container)
	k, ok := q.Provider.(*queueconfig.Kafka)
	require.True(t, ok, "provider is not *Kafka: %T", q.Provider)
	assert.Equal(t, "KAFKA_BROKERS", k.BrokersEnv)
	assert.Equal(t, "ORDERS_TOPIC", k.SourceEnv)
	assert.Equal(t, "KAFKA_GROUP", k.GroupEnv)
	assert.Equal(t, queueconfig.OffsetResetEarliest, k.OffsetReset)
	require.NotNil(t, k.TLS)
	assert.Equal(t, "kafka-ca", k.TLS.CASecret)
	assert.Equal(t, "ca.crt", k.TLS.CAKey)
	require.NotNil(t, k.SASL)
	assert.Equal(t, queueconfig.SASLMechanismScramSHA512, k.SASL.Mechanism)
	assert.Equal(t, "KAFKA_USERNAME", k.SASL.UsernameEnv)
	assert.Equal(t, "KAFKA_PASSWORD", k.SASL.PasswordEnv)
}

func TestParseRabbitMQFields(t *testing.T) {
	cfg, err := queueconfig.Parse(rabbitMQQueueYAML)
	require.NoError(t, err)
	require.Len(t, cfg.Queues, 1)

	q := cfg.Queues[0]
	assert.Equal(t, "invoices", q.Name)
	r, ok := q.Provider.(*queueconfig.RabbitMQ)
	require.True(t, ok, "provider is not *RabbitMQ: %T", q.Provider)
	assert.Equal(t, "AMQP_URL", r.URLEnv)
	assert.Equal(t, "RABBITMQ_MANAGEMENT_URL", r.ManagementURLEnv)
	assert.Equal(t, "INVOICE_QUEUE", r.SourceEnv)
	require.NotNil(t, r.TLS)
	assert.Equal(t, "rabbitmq-ca", r.TLS.CASecret)
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "empty document",
			yaml:    ``,
			wantErr: "at least one queue is required",
		},
		{
			name: "unknown field",
			yaml: `
queues:
  - name: orders
    container: app
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
      bogusField: nope
`,
			wantErr: "bogusField",
		},
		{
			name: "duplicate queue name",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: OTHER_TOPIC
      groupEnv: OTHER_GROUP
      offsetReset: earliest
`,
			wantErr: `queue "orders": name is not unique`,
		},
		{
			name: "invalid queue name",
			yaml: `
queues:
  - name: Not_A_Label
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
`,
			wantErr: "name",
		},
		{
			name: "neither provider configured",
			yaml: `
queues:
  - name: orders
`,
			wantErr: "must configure exactly one of kafka or rabbitmq",
		},
		{
			name: "both providers configured",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
    rabbitmq:
      urlEnv: AMQP_URL
      managementUrlEnv: RABBITMQ_MANAGEMENT_URL
      sourceEnv: ORDERS_QUEUE
`,
			wantErr: "found kafka and rabbitmq",
		},
		{
			name: "bad offsetReset",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: sideways
`,
			wantErr: `offsetReset must be "earliest" or "latest"`,
		},
		{
			name: "missing kafka required fields",
			yaml: `
queues:
  - name: orders
    kafka:
      offsetReset: earliest
`,
			wantErr: "kafka.brokersEnv is required",
		},
		{
			name: "missing rabbitmq required fields",
			yaml: `
queues:
  - name: invoices
    rabbitmq:
      urlEnv: AMQP_URL
`,
			wantErr: "rabbitmq.managementUrlEnv is required",
		},
		{
			name: "tls missing caKey",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
      tls:
        caSecret: kafka-ca
`,
			wantErr: "kafka.tls.caKey is required",
		},
		{
			name: "bad sasl mechanism",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
      sasl:
        mechanism: MD5
        usernameEnv: U
        passwordEnv: P
`,
			wantErr: "kafka.sasl.mechanism must be one of",
		},
		{
			name: "sasl missing username/password env",
			yaml: `
queues:
  - name: orders
    kafka:
      brokersEnv: KAFKA_BROKERS
      sourceEnv: ORDERS_TOPIC
      groupEnv: KAFKA_GROUP
      offsetReset: earliest
      sasl:
        mechanism: PLAIN
`,
			wantErr: "kafka.sasl.usernameEnv is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := queueconfig.Parse(tt.yaml)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestQueueValidateSchemaValid(t *testing.T) {
	cfg, err := queueconfig.Parse(kafkaQueueYAML)
	require.NoError(t, err)
	assert.NoError(t, cfg.Queues[0].ValidateSchema())
}

func TestQueueValidateSchemaInvalid(t *testing.T) {
	q := queueconfig.Queue{Name: "Not_A_Label"}
	err := q.ValidateSchema()
	require.Error(t, err)
	assert.ErrorContains(t, err, "name")
	assert.ErrorContains(t, err, "must configure exactly one of kafka or rabbitmq")
}

func TestQueueOverrideEnvNames(t *testing.T) {
	cfg, err := queueconfig.Parse(kafkaQueueYAML)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ORDERS_TOPIC", "KAFKA_GROUP"}, cfg.Queues[0].OverrideEnvNames())

	cfg, err = queueconfig.Parse(rabbitMQQueueYAML)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"INVOICE_QUEUE"}, cfg.Queues[0].OverrideEnvNames())

	var empty queueconfig.Queue
	assert.Empty(t, empty.OverrideEnvNames())
}

func TestQueueConnectionEnvNames(t *testing.T) {
	cfg, err := queueconfig.Parse(kafkaQueueYAML)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{"KAFKA_BROKERS", "KAFKA_USERNAME", "KAFKA_PASSWORD"}, cfg.Queues[0].ConnectionEnvNames())

	cfg, err = queueconfig.Parse(rabbitMQQueueYAML)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"AMQP_URL", "RABBITMQ_MANAGEMENT_URL"}, cfg.Queues[0].ConnectionEnvNames())

	var empty queueconfig.Queue
	assert.Empty(t, empty.ConnectionEnvNames())
}
