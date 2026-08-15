// Package queueconfig implements the schema, parsing, and static validation for the
// telepresence.io/queue-config annotation. The annotation declares logical queues that a
// workload's containers consume from a Kafka or RabbitMQ broker, so that a queue-agent can be
// inserted into the consumption stream on the workload's behalf.
package queueconfig

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

// Config is the decoded form of the telepresence.io/queue-config annotation.
type Config struct {
	Queues []Queue `json:"queues"`
}

// Queue declares one logical queue backed by exactly one provider.
type Queue struct {
	// Name identifies the queue to the CLI and must be unique within the declaration.
	Name string

	// Container names the app container that owns the queue's env vars. It is required
	// unless the workload has exactly one app container.
	Container string

	// Provider appears in the annotation as a field named after its provider kind.
	Provider Provider
}

// Kafka is the Kafka-specific configuration of a Queue.
type Kafka struct {
	// BrokersEnv names the container env var holding the broker address list.
	BrokersEnv string `json:"brokersEnv"`

	// SourceEnv names the container env var holding the source topic. It must be a
	// literal EnvVar; the manager reads the original topic from it and the webhook
	// shadows it in admitted Pods.
	SourceEnv string `json:"sourceEnv"`

	// GroupEnv names the container env var holding the consumer group. It must be a
	// literal EnvVar; safe startup and handoff require the original group's committed
	// offsets.
	GroupEnv string `json:"groupEnv"`

	// OffsetReset is "earliest" or "latest" and defines the start position for a
	// partition with no committed application offset.
	OffsetReset string `json:"offsetReset"`

	// TLS configures the CA used to verify the broker's certificate.
	TLS *TLS `json:"tls,omitempty"`

	// SASL configures SASL authentication against the broker.
	SASL *SASL `json:"sasl,omitempty"`
}

// RabbitMQ is the RabbitMQ-specific configuration of a Queue.
type RabbitMQ struct {
	// URLEnv names the container env var holding the AMQP URL.
	URLEnv string `json:"urlEnv"`

	// ManagementURLEnv names the container env var holding the HTTP management API URL,
	// used to inspect queue type, declaration arguments, and consumer/backlog counts.
	ManagementURLEnv string `json:"managementUrlEnv"`

	// SourceEnv names the container env var holding the source queue name. It must be a
	// literal EnvVar; the manager reads the original queue from it and the webhook
	// shadows it in admitted Pods.
	SourceEnv string `json:"sourceEnv"`

	// TLS configures the CA used to verify the broker's certificate.
	TLS *TLS `json:"tls,omitempty"`
}

// TLS names the Secret and key that hold a broker's CA certificate.
type TLS struct {
	// CASecret is the name of a Secret in the workload namespace, already referenced by
	// the selected app container's env or mounted volumes.
	CASecret string `json:"caSecret"`

	// CAKey is the key within CASecret that holds the CA certificate.
	CAKey string `json:"caKey"`
}

// SASL configures SASL authentication for a Kafka queue.
type SASL struct {
	// Mechanism is one of "PLAIN", "SCRAM-SHA-256", or "SCRAM-SHA-512".
	Mechanism string `json:"mechanism"`

	// UsernameEnv names the container env var holding the SASL username.
	UsernameEnv string `json:"usernameEnv"`

	// PasswordEnv names the container env var holding the SASL password.
	PasswordEnv string `json:"passwordEnv"`
}

// Provider is the provider-specific half of a Queue: the configuration block
// shaped after one broker kind. The interface is sealed — its methods are
// unexported — so the supported providers are exactly the implementations in
// this package. Adding one means implementing the interface and registering
// its block name in providerBlocks; no shared parsing or validation code
// changes.
type Provider interface {
	// blockField is the provider block's field name in the annotation.
	blockField() string

	// validateSchema applies the block's pod-template-independent rules.
	validateSchema(label string) []error

	// literalEnvs returns the env-var names that must be literal EnvVars in
	// the selected container, and that must not collide with any env name
	// used by another queue bound to the same container, each paired with
	// the schema field that names it.
	literalEnvs() []envRef

	// resolvableEnvs returns the connection and credential env-var names
	// that must be explicit, resolvable env entries in the selected
	// container, each paired with the schema field that names it.
	resolvableEnvs() []envRef

	// tlsConfig returns the block's TLS configuration, or nil.
	tlsConfig() *TLS
}

// envRef pairs an env-var name with the schema field that declared it, for
// error messages.
type envRef struct {
	field string
	name  string
}

// providerBlocks maps each provider block's annotation field name to its
// constructor. It is the registry Queue.UnmarshalJSON resolves block keys
// against.
//
//nolint:gochecknoglobals // immutable provider registry, not mutable state
var providerBlocks = map[string]func() Provider{
	"kafka":    func() Provider { return new(Kafka) },
	"rabbitmq": func() Provider { return new(RabbitMQ) },
}

// providerFields renders the registry's block names for error messages.
//
//nolint:gochecknoglobals // immutable rendering of providerBlocks
var providerFields = strings.Join(slices.Sorted(maps.Keys(providerBlocks)), " or ")

// UnmarshalJSON decodes a queue entry. The scalar fields decode by name;
// every other field must be a provider block registered in providerBlocks,
// and exactly one such block must be present. The block decodes strictly —
// unknown fields inside it are errors — and becomes the queue's Provider.
func (q *Queue) UnmarshalJSON(data []byte) error {
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(data, &raw, false); err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		val := raw[key]
		switch key {
		case "name":
			if err := json.Unmarshal(val, &q.Name, false); err != nil {
				return err
			}
		case "container":
			if err := json.Unmarshal(val, &q.Container, false); err != nil {
				return err
			}
		default:
			mk, ok := providerBlocks[key]
			if !ok {
				return fmt.Errorf("unknown field %q in queue", key)
			}
			if q.Provider != nil {
				return fmt.Errorf("must configure exactly one of %s; found %s and %s",
					providerFields, q.Provider.blockField(), key)
			}
			p := mk()
			if err := json.Unmarshal(val, p, true); err != nil {
				return err
			}
			q.Provider = p
		}
	}
	if q.Provider == nil {
		label := "queue"
		if q.Name != "" {
			label = fmt.Sprintf("queue %q", q.Name)
		}
		return fmt.Errorf("%s: must configure exactly one of %s", label, providerFields)
	}
	return nil
}

// MarshalJSON encodes a queue entry in its annotation form: the scalar
// fields by name, and the Provider as a field named after its block.
func (q Queue) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 3)
	if q.Name != "" {
		m["name"] = q.Name
	}
	if q.Container != "" {
		m["container"] = q.Container
	}
	if q.Provider != nil {
		m[q.Provider.blockField()] = q.Provider
	}
	return json.Marshal(m)
}

// Valid values for Kafka.OffsetReset.
const (
	OffsetResetEarliest = "earliest"
	OffsetResetLatest   = "latest"
)

// Valid values for SASL.Mechanism.
const (
	SASLMechanismPlain       = "PLAIN"
	SASLMechanismScramSHA256 = "SCRAM-SHA-256"
	SASLMechanismScramSHA512 = "SCRAM-SHA-512"
)

// Parse decodes the value of a telepresence.io/queue-config annotation and applies the
// static, pod-template-independent validation rules from the schema: unknown fields, queue
// name uniqueness and format, provider selection, required fields, and enumerations. It
// returns an error that accumulates every violation found.
func Parse(value string) (*Config, error) {
	data, err := yaml.YAMLToJSON([]byte(value))
	if err != nil {
		return nil, fmt.Errorf("queue-config: %w", err)
	}
	cfg := new(Config)
	if err := json.Unmarshal(data, cfg, true); err != nil {
		return nil, fmt.Errorf("queue-config: %w", err)
	}
	if err := cfg.validateSchema(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// queueLabel identifies a queue in an error message. It uses the declared name when the name
// itself is not the problem, and falls back to a positional reference otherwise.
func queueLabel(i int, name string) string {
	if name == "" {
		return fmt.Sprintf("queue[%d]", i)
	}
	return fmt.Sprintf("queue %q", name)
}

// label identifies q in an error message: the declared name when non-empty, or a generic
// fallback when there is no name to use instead.
func (q *Queue) label() string {
	if q.Name == "" {
		return "queue"
	}
	return fmt.Sprintf("queue %q", q.Name)
}

// ValidateSchema applies the queue-level rules from the annotation schema to q alone: name
// presence and RFC-1123 format, provider presence, and the provider's own validateSchema
// rules. It returns an error that accumulates every violation found, labeled with the
// queue's name.
func (q *Queue) ValidateSchema() error {
	label := q.label()
	var errs []error

	if q.Name == "" {
		errs = append(errs, fmt.Errorf("%s: name is required", label))
	} else if verrs := validation.IsDNS1123Label(q.Name); len(verrs) > 0 {
		errs = append(errs, fmt.Errorf("%s: name %q is invalid: %s", label, q.Name, verrs[0]))
	}

	if p := q.Provider; p == nil {
		errs = append(errs, fmt.Errorf("%s: must configure exactly one of %s", label, providerFields))
	} else {
		errs = append(errs, p.validateSchema(label)...)
	}
	return errors.Join(errs...)
}

// OverrideEnvNames returns the names of the env vars this queue's provider overrides in the
// application's admission-time environment: sourceEnv and, for Kafka, groupEnv. It returns an
// empty slice when q.Provider is nil.
func (q *Queue) OverrideEnvNames() []string {
	if q.Provider == nil {
		return []string{}
	}
	return envRefNames(q.Provider.literalEnvs())
}

// ConnectionEnvNames returns the names of the connection and credential env vars this queue's
// provider requires from the selected container. It returns an empty slice when q.Provider is
// nil.
func (q *Queue) ConnectionEnvNames() []string {
	if q.Provider == nil {
		return []string{}
	}
	return envRefNames(q.Provider.resolvableEnvs())
}

// envRefNames renders refs' names in order.
func envRefNames(refs []envRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.name
	}
	return names
}

// validateSchema applies the pod-template-independent rules from the annotation schema.
func (c *Config) validateSchema() error {
	var errs []error
	if len(c.Queues) == 0 {
		errs = append(errs, errors.New("queue-config: at least one queue is required"))
	}

	names := make(map[string]bool, len(c.Queues))

	for i := range c.Queues {
		q := &c.Queues[i]
		if err := q.ValidateSchema(); err != nil {
			errs = append(errs, err)
		}

		// A name that failed ValidateSchema's own presence/format check does not
		// participate in uniqueness tracking.
		if q.Name == "" || len(validation.IsDNS1123Label(q.Name)) > 0 {
			continue
		}
		if names[q.Name] {
			errs = append(errs, fmt.Errorf("%s: name is not unique", queueLabel(i, q.Name)))
		} else {
			names[q.Name] = true
		}
	}
	return errors.Join(errs...)
}

func (k *Kafka) blockField() string {
	return "kafka"
}

func (k *Kafka) literalEnvs() []envRef {
	return []envRef{
		{field: "kafka.sourceEnv", name: k.SourceEnv},
		{field: "kafka.groupEnv", name: k.GroupEnv},
	}
}

func (k *Kafka) resolvableEnvs() []envRef {
	ers := []envRef{{field: "kafka.brokersEnv", name: k.BrokersEnv}}
	if k.SASL != nil {
		ers = append(ers,
			envRef{field: "kafka.sasl.usernameEnv", name: k.SASL.UsernameEnv},
			envRef{field: "kafka.sasl.passwordEnv", name: k.SASL.PasswordEnv})
	}
	return ers
}

func (k *Kafka) tlsConfig() *TLS {
	return k.TLS
}

func (k *Kafka) validateSchema(label string) []error {
	var errs []error
	if k.BrokersEnv == "" {
		errs = append(errs, fmt.Errorf("%s: kafka.brokersEnv is required", label))
	}
	if k.SourceEnv == "" {
		errs = append(errs, fmt.Errorf("%s: kafka.sourceEnv is required", label))
	}
	if k.GroupEnv == "" {
		errs = append(errs, fmt.Errorf("%s: kafka.groupEnv is required", label))
	}
	switch k.OffsetReset {
	case OffsetResetEarliest, OffsetResetLatest:
	default:
		errs = append(errs, fmt.Errorf("%s: kafka.offsetReset must be %q or %q, got %q",
			label, OffsetResetEarliest, OffsetResetLatest, k.OffsetReset))
	}
	if k.TLS != nil {
		errs = append(errs, k.TLS.validateSchema(label, "kafka.tls")...)
	}
	if k.SASL != nil {
		errs = append(errs, k.SASL.validateSchema(label)...)
	}
	return errs
}

func (r *RabbitMQ) blockField() string {
	return "rabbitmq"
}

func (r *RabbitMQ) literalEnvs() []envRef {
	return []envRef{{field: "rabbitmq.sourceEnv", name: r.SourceEnv}}
}

func (r *RabbitMQ) resolvableEnvs() []envRef {
	return []envRef{
		{field: "rabbitmq.urlEnv", name: r.URLEnv},
		{field: "rabbitmq.managementUrlEnv", name: r.ManagementURLEnv},
	}
}

func (r *RabbitMQ) tlsConfig() *TLS {
	return r.TLS
}

func (r *RabbitMQ) validateSchema(label string) []error {
	var errs []error
	if r.URLEnv == "" {
		errs = append(errs, fmt.Errorf("%s: rabbitmq.urlEnv is required", label))
	}
	if r.ManagementURLEnv == "" {
		errs = append(errs, fmt.Errorf("%s: rabbitmq.managementUrlEnv is required", label))
	}
	if r.SourceEnv == "" {
		errs = append(errs, fmt.Errorf("%s: rabbitmq.sourceEnv is required", label))
	}
	if r.TLS != nil {
		errs = append(errs, r.TLS.validateSchema(label, "rabbitmq.tls")...)
	}
	return errs
}

func (t *TLS) validateSchema(label, field string) []error {
	var errs []error
	if t.CASecret == "" {
		errs = append(errs, fmt.Errorf("%s: %s.caSecret is required", label, field))
	}
	if t.CAKey == "" {
		errs = append(errs, fmt.Errorf("%s: %s.caKey is required", label, field))
	}
	return errs
}

func (s *SASL) validateSchema(label string) []error {
	var errs []error
	switch s.Mechanism {
	case SASLMechanismPlain, SASLMechanismScramSHA256, SASLMechanismScramSHA512:
	default:
		errs = append(errs, fmt.Errorf("%s: kafka.sasl.mechanism must be one of %q, %q, %q, got %q",
			label, SASLMechanismPlain, SASLMechanismScramSHA256, SASLMechanismScramSHA512, s.Mechanism))
	}
	if s.UsernameEnv == "" {
		errs = append(errs, fmt.Errorf("%s: kafka.sasl.usernameEnv is required", label))
	}
	if s.PasswordEnv == "" {
		errs = append(errs, fmt.Errorf("%s: kafka.sasl.passwordEnv is required", label))
	}
	return errs
}
