package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DesiredState is the operator-requested state of a KafkaSplit.
// +kubebuilder:validation:Enum=Enabled;Disabled
type DesiredState string

const (
	DesiredStateEnabled  DesiredState = "Enabled"
	DesiredStateDisabled DesiredState = "Disabled"
)

// ShadowMode controls who provisions Kafka shadow topics.
// +kubebuilder:validation:Enum=Managed;Preprovisioned
type ShadowMode string

const (
	ShadowModeManaged        ShadowMode = "Managed"
	ShadowModePreprovisioned ShadowMode = "Preprovisioned"
)

// RouteState is the desired lifecycle state of a KafkaRoute.
// +kubebuilder:validation:Enum=Active;Closing
type RouteState string

const (
	RouteStateActive  RouteState = "Active"
	RouteStateClosing RouteState = "Closing"
)

// KafkaAdmissionMode controls how replacement Pods are admitted.
// +kubebuilder:validation:Enum=Normal;Shadow
type KafkaAdmissionMode string

const (
	KafkaAdmissionNormal KafkaAdmissionMode = "Normal"
	KafkaAdmissionShadow KafkaAdmissionMode = "Shadow"
)

// ValueSource is a literal or one key in a Secret.
type ValueSource struct {
	Value        string                    `json:"value,omitempty"`
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// KafkaTLSSpec configures broker TLS and optional client authentication.
type KafkaTLSSpec struct {
	CA          *corev1.SecretKeySelector `json:"ca,omitempty"`
	Certificate *corev1.SecretKeySelector `json:"certificate,omitempty"`
	PrivateKey  *corev1.SecretKeySelector `json:"privateKey,omitempty"`
	ServerName  string                    `json:"serverName,omitempty"`
}

// KafkaOAuthSpec configures an OAUTHBEARER client-credentials flow.
type KafkaOAuthSpec struct {
	TokenURL     string      `json:"tokenURL"`
	ClientID     ValueSource `json:"clientID"`
	ClientSecret ValueSource `json:"clientSecret"`
	Scopes       []string    `json:"scopes,omitempty"`
}

// KafkaKerberosSpec configures SASL/GSSAPI.
type KafkaKerberosSpec struct {
	ServiceName string                    `json:"serviceName"`
	Realm       string                    `json:"realm"`
	Username    ValueSource               `json:"username"`
	Password    *ValueSource              `json:"password,omitempty"`
	Keytab      *corev1.SecretKeySelector `json:"keytab,omitempty"`
	Config      *corev1.SecretKeySelector `json:"config"`
}

// KafkaAWSSpec configures AWS MSK IAM authentication.
type KafkaAWSSpec struct {
	Region string `json:"region"`
}

// KafkaSASLSpec configures one Kafka SASL mechanism.
type KafkaSASLSpec struct {
	// +kubebuilder:validation:Enum=PLAIN;SCRAM-SHA-256;SCRAM-SHA-512;OAUTHBEARER;GSSAPI;AWS_MSK_IAM
	Mechanism string             `json:"mechanism"`
	Username  *ValueSource       `json:"username,omitempty"`
	Password  *ValueSource       `json:"password,omitempty"`
	OAuth     *KafkaOAuthSpec    `json:"oauth,omitempty"`
	Kerberos  *KafkaKerberosSpec `json:"kerberos,omitempty"`
	AWS       *KafkaAWSSpec      `json:"aws,omitempty"`
}

// KafkaConnectionSpec contains provider-only broker connection settings.
type KafkaConnectionSpec struct {
	// +kubebuilder:validation:MinItems=1
	BootstrapServers []string       `json:"bootstrapServers"`
	TLS              *KafkaTLSSpec  `json:"tls,omitempty"`
	SASL             *KafkaSASLSpec `json:"sasl,omitempty"`
}

// KafkaSourceSpec identifies the application subscription being split.
type KafkaSourceSpec struct {
	Group string `json:"group"`
	// +kubebuilder:validation:MinItems=1
	Topics []string `json:"topics"`
	// +kubebuilder:validation:Enum=earliest;latest
	OffsetReset string `json:"offsetReset"`
}

// KafkaTopicEnvBinding maps one source topic to an application variable.
type KafkaTopicEnvBinding struct {
	Source string `json:"source"`
	Env    string `json:"env"`
}

// KafkaApplicationSpec describes the selected container's configuration
// adapter.
type KafkaApplicationSpec struct {
	TopicEnv           string                 `json:"topicEnv,omitempty"`
	TopicSeparator     string                 `json:"topicSeparator,omitempty"`
	TopicBindings      []KafkaTopicEnvBinding `json:"topicBindings,omitempty"`
	GroupEnv           string                 `json:"groupEnv"`
	IsolationLevelEnv  string                 `json:"isolationLevelEnv"`
	TransactionalIDEnv string                 `json:"transactionalIDEnv,omitempty"`
	ShadowCredentials  map[string]ValueSource `json:"shadowCredentials,omitempty"`
}

// KafkaSplitterSpec controls data-plane capacity.
type KafkaSplitterSpec struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100
	BatchSize int32 `json:"batchSize,omitempty"`
}

// EffectiveReplicas returns the configured replica count, defaulting to 1.
func (s KafkaSplitterSpec) EffectiveReplicas() int32 {
	if s.Replicas <= 0 {
		return 1
	}
	return s.Replicas
}

// EffectiveBatchSize returns the configured batch size, defaulting to 100.
func (s KafkaSplitterSpec) EffectiveBatchSize() int32 {
	if s.BatchSize <= 0 {
		return 100
	}
	return s.BatchSize
}

// KafkaManagedShadows configures controller-created topics.
type KafkaManagedShadows struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=32767
	ReplicationFactor *int32            `json:"replicationFactor,omitempty"`
	Configs           map[string]string `json:"configs,omitempty"`
}

// KafkaSessionSlot is one reusable preprovisioned set of session topics.
type KafkaSessionSlot struct {
	Name   string            `json:"name"`
	Group  string            `json:"group"`
	Topics map[string]string `json:"topics"`
}

// KafkaPreprovisionedShadows lists operator-owned application and session
// resources.
type KafkaPreprovisionedShadows struct {
	ApplicationGroup  string             `json:"applicationGroup"`
	ApplicationTopics map[string]string  `json:"applicationTopics"`
	Sessions          []KafkaSessionSlot `json:"sessions"`
}

// KafkaShadowSpec configures shadow resource ownership.
type KafkaShadowSpec struct {
	Mode           ShadowMode                  `json:"mode"`
	Managed        *KafkaManagedShadows        `json:"managed,omitempty"`
	Preprovisioned *KafkaPreprovisionedShadows `json:"preprovisioned,omitempty"`
}

// KafkaSplitSpec declares one original Kafka consumer group.
type KafkaSplitSpec struct {
	DesiredState     DesiredState         `json:"desiredState"`
	WorkloadSelector metav1.LabelSelector `json:"workloadSelector"`
	Container        string               `json:"container"`
	Connection       KafkaConnectionSpec  `json:"connection"`
	Source           KafkaSourceSpec      `json:"source"`
	Application      KafkaApplicationSpec `json:"application"`
	Splitter         KafkaSplitterSpec    `json:"splitter,omitempty"`
	Shadows          KafkaShadowSpec      `json:"shadows"`
}

// KafkaActiveSpec is the safety-relevant configuration snapshot currently
// enforced by admission and broker reconciliation.
type KafkaActiveSpec struct {
	Container   string               `json:"container"`
	Connection  KafkaConnectionSpec  `json:"connection"`
	Source      KafkaSourceSpec      `json:"source"`
	Application KafkaApplicationSpec `json:"application"`
	Splitter    KafkaSplitterSpec    `json:"splitter"`
	Shadows     KafkaShadowSpec      `json:"shadows"`
}

// WorkloadReference is an immutable member of the active workload snapshot.
type WorkloadReference struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
}

// KafkaTopicStatus identifies one source topic incarnation.
type KafkaTopicStatus struct {
	Name       string `json:"name"`
	TopicID    string `json:"topicID"`
	Partitions int32  `json:"partitions"`
}

// KafkaResourceStatus is one broker resource owned or leased by a split.
type KafkaResourceStatus struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	TopicID   string `json:"topicID,omitempty"`
	Source    string `json:"source,omitempty"`
	Managed   bool   `json:"managed"`
	RouteName string `json:"routeName,omitempty"`
}

// KafkaSplitterMemberStatus describes one acknowledged StatefulSet member.
type KafkaSplitterMemberStatus struct {
	Name       string      `json:"name"`
	Generation int64       `json:"generation"`
	Healthy    bool        `json:"healthy"`
	LastSeen   metav1.Time `json:"lastSeen"`
}

// SplitPhase is the reconciliation phase of a KafkaSplit.
type SplitPhase string

const (
	SplitPhasePreparing            SplitPhase = "Preparing"
	SplitPhaseRedirecting          SplitPhase = "Redirecting"
	SplitPhaseStarting             SplitPhase = "Starting"
	SplitPhaseEnabled              SplitPhase = "Enabled"
	SplitPhasePausing              SplitPhase = "Pausing"
	SplitPhaseClosingRoutes        SplitPhase = "ClosingRoutes"
	SplitPhaseDrainingApplication  SplitPhase = "DrainingApplication"
	SplitPhaseStoppingSplitter     SplitPhase = "StoppingSplitter"
	SplitPhaseRestoringApplication SplitPhase = "RestoringApplication"
	SplitPhaseCleaningApplication  SplitPhase = "CleaningApplication"
	SplitPhaseDisabled             SplitPhase = "Disabled"
	SplitPhaseDegraded             SplitPhase = "Degraded"
	SplitPhasePending              SplitPhase = "Pending"
	SplitPhaseInvalid              SplitPhase = "Invalid"
)

// AcceptsRoutes reports whether routes may be provisioned while a split is in this phase.
func (p SplitPhase) AcceptsRoutes() bool {
	return p == SplitPhaseEnabled || p == SplitPhaseStarting
}

// KafkaSplitStatus records durable reconciliation and broker state.
type KafkaSplitStatus struct {
	ObservedGeneration int64                       `json:"observedGeneration,omitempty"`
	ActiveGeneration   int64                       `json:"activeGeneration,omitempty"`
	ActiveSpec         *KafkaActiveSpec            `json:"activeSpec,omitempty"`
	Phase              SplitPhase                  `json:"phase,omitempty"`
	AdmissionMode      KafkaAdmissionMode          `json:"admissionMode,omitempty"`
	ApplicationEnv     map[string]string           `json:"applicationEnv,omitempty"`
	ApplicationTopics  map[string]string           `json:"applicationTopics,omitempty"`
	ApplicationGroup   string                      `json:"applicationGroup,omitempty"`
	TransactionalID    string                      `json:"transactionalIDPrefix,omitempty"`
	SplitterName       string                      `json:"splitterName,omitempty"`
	Workloads          []WorkloadReference         `json:"workloads,omitempty"`
	SourceTopics       []KafkaTopicStatus          `json:"sourceTopics,omitempty"`
	Resources          []KafkaResourceStatus       `json:"resources,omitempty"`
	RouteGeneration    int64                       `json:"routeGeneration,omitempty"`
	Members            []KafkaSplitterMemberStatus `json:"members,omitempty"`
	ApplicationLag     *int64                      `json:"applicationLag,omitempty"`
	Conditions         []metav1.Condition          `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=splits,scope=Namespaced,shortName=ksplit,singular=split
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.spec.desiredState`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KafkaSplit is one standing Kafka consumer-group split.
type KafkaSplit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KafkaSplitSpec   `json:"spec,omitempty"`
	Status            KafkaSplitStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KafkaSplitList contains KafkaSplit resources.
type KafkaSplitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KafkaSplit `json:"items"`
}

// KafkaHeaderMatch is one exact last-header predicate.
type KafkaHeaderMatch struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// KafkaRoutePredicate is an AND predicate over Kafka metadata.
type KafkaRoutePredicate struct {
	Headers   []KafkaHeaderMatch `json:"headers,omitempty"`
	Key       []byte             `json:"key,omitempty"`
	KeyPrefix []byte             `json:"keyPrefix,omitempty"`
}

// KafkaRouteSpec describes one personal route.
type KafkaRouteSpec struct {
	SplitRef     corev1.LocalObjectReference `json:"splitRef"`
	AttachmentID string                      `json:"attachmentID"`
	SessionID    string                      `json:"sessionID"`
	ExpiresAt    metav1.Time                 `json:"expiresAt"`
	DesiredState RouteState                  `json:"desiredState"`
	Predicate    KafkaRoutePredicate         `json:"predicate,omitempty"`
}

// RoutePhase is the reconciliation phase of a KafkaRoute.
type RoutePhase string

const (
	RoutePhasePending  RoutePhase = "Pending"
	RoutePhaseStaged   RoutePhase = "Staged"
	RoutePhaseReady    RoutePhase = "Ready"
	RoutePhaseClosing  RoutePhase = "Closing"
	RoutePhaseCleaning RoutePhase = "Cleaning"
	RoutePhaseClosed   RoutePhase = "Closed"
	RoutePhaseInvalid  RoutePhase = "Invalid"
)

// KafkaRouteStatus records route resources and readiness.
type KafkaRouteStatus struct {
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Phase              RoutePhase            `json:"phase,omitempty"`
	Group              string                `json:"group,omitempty"`
	Topics             map[string]string     `json:"topics,omitempty"`
	Environment        map[string]string     `json:"environment,omitempty"`
	Resources          []KafkaResourceStatus `json:"resources,omitempty"`
	RouteGeneration    int64                 `json:"routeGeneration,omitempty"`
	Lag                *int64                `json:"lag,omitempty"`
	Conditions         []metav1.Condition    `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=routes,scope=Namespaced,shortName=kroute,singular=route
// +kubebuilder:printcolumn:name="Split",type=string,JSONPath=`.spec.splitRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KafkaRoute is one durable personal route for a KafkaSplit.
type KafkaRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KafkaRouteSpec   `json:"spec,omitempty"`
	Status            KafkaRouteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KafkaRouteList contains KafkaRoute resources.
type KafkaRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KafkaRoute `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KafkaSplit{}, &KafkaSplitList{}, &KafkaRoute{}, &KafkaRouteList{})
}
