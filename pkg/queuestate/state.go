// Package queuestate defines the durable desired-state document for queue
// splitting, the per-queue phase state machine, filter overlap detection, and
// bounded deterministic naming for the broker and Kubernetes resources a split
// creates. The package is pure logic: it has no Kubernetes client, gRPC, or
// broker dependency.
//
// A [WorkloadState] is the schema persisted in a manager-owned ConfigMap, one
// per workload with an active or recoverable split, keyed by
// [StateConfigMapDataKey] in the ConfigMap's Data map.
package queuestate

import (
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/queueconfig"
)

// StateConfigMapDataKey is the key under which a WorkloadState's YAML
// encoding is stored in the state ConfigMap's Data map.
const StateConfigMapDataKey = "queue-state.yaml"

// WorkloadKind identifies the kind of workload a WorkloadState belongs to.
type WorkloadKind string

const (
	// WorkloadKindDeployment is a Kubernetes Deployment.
	WorkloadKindDeployment WorkloadKind = "Deployment"

	// WorkloadKindStatefulSet is a Kubernetes StatefulSet.
	WorkloadKindStatefulSet WorkloadKind = "StatefulSet"
)

// WorkloadState is the durable desired-state document for one workload's
// queue splits. It is the restart source of truth: a manager or queue-agent
// that starts with no other context reconstructs everything it needs from
// this document.
type WorkloadState struct {
	// Generation increases on every persisted change to this document. A
	// consumer acknowledges a generation only after it has reconciled to it.
	Generation int64 `json:"generation,omitzero"`

	// InstallID identifies the traffic-manager installation that owns this
	// state. It is one of the inputs to every bounded resource name derived
	// from this document, so identically named workloads in different
	// installations never collide.
	InstallID string `json:"installID,omitzero"`

	// WorkloadUID is the Kubernetes UID of the Deployment or StatefulSet this
	// state belongs to.
	WorkloadUID string `json:"workloadUID,omitzero"`

	// WorkloadName is the name of the Deployment or StatefulSet this state
	// belongs to.
	WorkloadName string `json:"workloadName,omitzero"`

	// WorkloadKind is the kind of the workload this state belongs to.
	WorkloadKind WorkloadKind `json:"workloadKind,omitzero"`

	// Namespace is the namespace of the workload and of this document's own
	// ConfigMap.
	Namespace string `json:"namespace,omitzero"`

	// PreActivationReplicas is the replica count recorded before the
	// workload was last scaled to zero for a cutover. It is nil when no
	// cutover has recorded a count that still needs to be restored.
	PreActivationReplicas *int32 `json:"preActivationReplicas,omitempty"`

	// OverrideGeneration is the active override generation: the value the
	// admission webhook compares against a Pod's recorded generation to
	// decide whether that Pod already carries the queue environment
	// overrides for the current active set.
	OverrideGeneration int64 `json:"overrideGeneration,omitzero"`

	// Queues holds one entry per logical queue declared for this workload
	// that is active, being activated, being deactivated, or held degraded.
	// A logical queue with no entry has no queue-splitting state.
	Queues []QueueState `json:"queues,omitempty"`
}

// QueueState is the persisted state of one logical queue.
type QueueState struct {
	// Name is the logical queue name from the workload's queue-config
	// annotation.
	Name string `json:"name,omitzero"`

	// Phase is this queue's current position in the activation/deactivation
	// state machine.
	Phase Phase `json:"phase,omitzero"`

	// ResumePhase is the phase to resume once the blocking condition
	// clears. It is set, together with BlockedReason, exactly while Phase
	// is Degraded.
	ResumePhase Phase `json:"resumePhase,omitzero"`

	// BlockedReason is a stable, machine-readable identifier of the
	// condition blocking progress -- not prose. It is set, together with
	// ResumePhase, exactly while Phase is Degraded.
	BlockedReason string `json:"blockedReason,omitzero"`

	// ActivationID identifies the activation epoch that created this
	// queue's current shadows and provider groups. It is one of the inputs
	// to every bounded resource name derived for this queue, so a
	// re-activation after a full deactivation never reuses a prior
	// activation's resource names.
	ActivationID string `json:"activationID,omitzero"`

	// Declaration is this queue's validated declaration, copied from the
	// annotation at activation. It is credential-free by construction: env
	// and Secret references name only, so it never carries a resolved
	// Secret value.
	Declaration queueconfig.Queue `json:"declaration,omitzero"`

	// Source is the resolved source name -- a Kafka topic or RabbitMQ
	// queue -- read from the app container's sourceEnv at activation.
	Source string `json:"source,omitzero"`

	// Group is the resolved Kafka consumer group read from the app
	// container's groupEnv at activation. It is empty for providers with
	// no consumer group.
	Group string `json:"group,omitzero"`

	// ConnectionEnv is the connection and credential env entries copied
	// verbatim from the validated app container. Each entry's source is a
	// literal value or a secretKeyRef/configMapKeyRef; this never holds a
	// resolved Secret value.
	ConnectionEnv []core.EnvVar `json:"connectionEnv,omitempty"`

	// EnvOverrides are the environment variable overrides this queue
	// contributes to the application's admission-time environment, keyed by
	// variable name.
	EnvOverrides map[string]string `json:"envOverrides,omitempty"`

	// Routes are the developer routes attached to this queue.
	Routes []Route `json:"routes,omitempty"`

	// BrokerResources lists every broker resource created for this queue's
	// current activation. Cleanup deletes only resources recorded here, so
	// a name collision with a user's own broker resource can never cause an
	// unrecorded resource to be deleted. A route record outlives its broker
	// resources: any route ID a recorded name derives from is still present
	// in Routes for as long as that resource is recorded here.
	BrokerResources []BrokerResource `json:"brokerResources,omitempty"`

	// Handoff carries an engine-opaque checkpoint produced by Stop and
	// consumed by DrainApplication and CommitHandoff. Its content is
	// provider-specific and this package never interprets it.
	Handoff []byte `json:"handoff,omitempty"`
}

// Route is one developer's attachment to a logical queue: a filter predicate
// and the shadow destination that messages matching it are copied to.
type Route struct {
	// ID is a stable, client-chosen identifier for this route. It survives
	// client reconnects and is one of the inputs to the session shadow's
	// bounded resource name.
	ID string `json:"id,omitzero"`

	// Owner is the durable identity of the session that created this route,
	// used to decide whether a reconnecting client may adopt it.
	Owner string `json:"owner,omitzero"`

	// Filter is the route's equality-conjunction predicate: a message
	// matches when every key present here maps to the same value in the
	// message. An empty filter matches every message.
	Filter map[string]string `json:"filter,omitempty"`

	// State is this route's position in its own drain lifecycle,
	// independent of the owning queue's Phase.
	State RouteState `json:"state,omitzero"`

	// Expiry is when an unrenewed route becomes eligible for draining. It is
	// independent of the manager RPC session ID that created the route, so a
	// disconnected client keeps its route until Expiry passes.
	Expiry metav1.Time `json:"expiry,omitzero"`
}

// ResourceKind identifies the broker-native shape of a BrokerResource.
type ResourceKind string

const (
	// ResourceKindTopic is a Kafka topic.
	ResourceKindTopic ResourceKind = "topic"

	// ResourceKindQueue is a RabbitMQ queue.
	ResourceKindQueue ResourceKind = "queue"

	// ResourceKindGroup is a Kafka consumer group.
	ResourceKindGroup ResourceKind = "group"
)

// BrokerResource records one broker-native resource created for a queue's
// activation, so cleanup can delete exactly the resources this package's
// naming functions created and nothing a broker user owns.
type BrokerResource struct {
	// Kind is the broker-native shape of the resource.
	Kind ResourceKind `json:"kind,omitzero"`

	// Name is the resource's bounded deterministic name.
	Name string `json:"name,omitzero"`
}

// Marshal returns the YAML encoding of the WorkloadState, suitable for
// storage under StateConfigMapDataKey in the state ConfigMap.
func (s *WorkloadState) Marshal() ([]byte, error) {
	return yaml.Marshal(s)
}

// UnmarshalYAML parses data, the YAML encoding produced by
// [*WorkloadState.Marshal], into a new WorkloadState. An unrecognized field
// is an error rather than being silently dropped, and the decoded document
// must also pass [*WorkloadState.Validate]: no unvalidated document enters
// the program through this function.
func UnmarshalYAML(data []byte) (*WorkloadState, error) {
	into := new(WorkloadState)
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, into, true); err != nil {
		return nil, err
	}
	if err := into.Validate(); err != nil {
		return nil, err
	}
	return into, nil
}
