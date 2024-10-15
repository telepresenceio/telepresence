package agentconfig

import (
	"reflect"

	"github.com/go-json-experiment/json"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
)

const (
	// ConfigMap is the name of the ConfigMap that contains the agent configs.
	ConfigMap = "telepresence-agents"

	ContainerName            = "traffic-agent"
	InitContainerName        = "tel-agent-init"
	AnnotationVolumeName     = "traffic-annotations"
	AnnotationMountPoint     = "/tel_pod_info"
	ConfigVolumeName         = "traffic-config"
	ConfigMountPoint         = "/etc/traffic-agent"
	TerminatingTLSVolumeName = "traffic-terminating-tls"
	TerminatingTLSMountPoint = "/terminating_tls"
	OriginatingTLSVolumeName = "traffic-originating-tls"
	OriginatingTLSMountPoint = "/originating_tls"
	ConfigFile               = "config.yaml"
	MountPrefixApp           = "/tel_app_mounts"
	ExportsVolumeName        = "export-volume"
	ExportsMountPoint        = "/tel_app_exports"
	TempVolumeName           = "tel-agent-tmp"
	TempMountPoint           = "/tmp"
	EnvPrefix                = "_TEL_"
	EnvPrefixAgent           = EnvPrefix + "AGENT_"
	EnvPrefixApp             = EnvPrefix + "APP_"

	// EnvInterceptContainer intercepted container propagated to client during intercept.
	EnvInterceptContainer = "TELEPRESENCE_CONTAINER"

	// EnvInterceptMounts mount points propagated to client during intercept.
	EnvInterceptMounts = "TELEPRESENCE_MOUNTS"

	// EnvAPIPort is the port number of the Telepresence API server, when it is enabled.
	EnvAPIPort = "TELEPRESENCE_API_PORT"

	DomainPrefix                         = "telepresence.getambassador.io/"
	InjectAnnotation                     = DomainPrefix + "inject-" + ContainerName
	InjectIgnoreVolumeMounts             = DomainPrefix + "inject-ignore-volume-mounts"
	TerminatingTLSSecretAnnotation       = DomainPrefix + "inject-terminating-tls-secret"
	OriginatingTLSSecretAnnotation       = DomainPrefix + "inject-originating-tls-secret"
	LegacyTerminatingTLSSecretAnnotation = "getambassador.io/inject-terminating-tls-secret"
	LegacyOriginatingTLSSecretAnnotation = "getambassador.io/inject-originating-tls-secret"
	WorkloadNameLabel                    = "telepresence.io/workloadName"
	WorkloadKindLabel                    = "telepresence.io/workloadKind"
	WorkloadEnabledLabel                 = "telepresence.io/workloadEnabled"
	K8SCreatedByLabel                    = "app.kubernetes.io/created-by"
)

type ReplacePolicy bool

func (r *ReplacePolicy) UnmarshalJSON(data []byte) error {
	var i int
	if err := json.Unmarshal(data, &i); err != nil {
		// Allow true/false too.
		var v bool
		if boolErr := json.Unmarshal(data, &v); boolErr != nil {
			return err
		}
		*r = ReplacePolicy(v)
	} else {
		*r = i == 1
	}
	return nil
}

func (r ReplacePolicy) MarshalJSON() ([]byte, error) {
	i := 0
	if r {
		i = 1
	}
	return json.Marshal(&i)
}

// Intercept describes the mapping between a service port and an intercepted container port or, when
// service is used, just the container port.
type Intercept struct {
	// The name of the intercepted container port
	ContainerPortName string `json:"containerPortName,omitzero"`

	// Name of intercepted service
	ServiceName string `json:"serviceName,omitzero"`

	// UID of intercepted service
	ServiceUID types.UID `json:"serviceUID,omitzero"`

	// Name of intercepted service port
	ServicePortName string `json:"servicePortName,omitzero"`

	// TargetPortNumeric is set to true unless the servicePort has a symbolic target port
	TargetPortNumeric bool `json:"targetPortNumeric,omitzero"`

	// L4 protocol used by the intercepted port
	Protocol core.Protocol `json:"protocol,omitzero"`

	// L7 protocol used by the intercepted port
	AppProtocol string `json:"appProtocol,omitzero"`

	// True if the service is headless
	Headless bool `json:"headless,omitzero"`

	// The number of the intercepted container port
	ContainerPort uint16 `json:"containerPort,omitzero"`

	// Number of intercepted service port
	ServicePort uint16 `json:"servicePort,omitzero"`

	// The port number that the agent listens to
	AgentPort uint16 `json:"agentPort,omitzero"`
}

// Container describes one container that can have one or several intercepts.
type Container struct {
	// Name of the intercepted container
	Name string `json:"name,omitempty" yaml:"name,omitzero"`

	// The intercepts managed by the agent
	Intercepts []*Intercept `json:"intercepts,omitempty"`

	// Prefix used for all keys in the container environment copy
	EnvPrefix string `json:"envPrefix,omitzero"`

	// Where the agent mounts the agents volumes
	MountPoint string `json:"mountPoint,omitzero"`

	// Mounts are the actual mount points that are mounted by this container
	Mounts []string `json:"Mounts,omitempty"`

	// Replace is whether the agent should replace the intercepted container
	Replace ReplacePolicy `json:"replace,omitzero"`
}

// The Sidecar configures the traffic-agent sidecar.
type Sidecar struct {
	// If Create is true, then this Config has not yet been filled in.
	Create bool `json:"create,omitzero"`

	// If Manual is true, then this Config is created manually
	Manual bool `json:"manual,omitzero"`

	// The fully qualified name of the traffic-agent image, i.e. "ghcr.io/telepresenceio/tel2:2.5.4"
	AgentImage string `json:"agentImage,omitzero"`

	// One of "IfNotPresent", "Always", or "Never"
	PullPolicy string `json:"pullPolicy,omitzero"`

	// Secrets used when pulling the agent image from a private registry
	PullSecrets []core.LocalObjectReference `json:"pullSecrets,omitempty"`

	// The name of the traffic-agent instance. Typically, the same as the name of the workload owner
	AgentName string `json:"agentName,omitzero"`

	// The namespace of the intercepted pod
	Namespace string `json:"namespace,omitzero"`

	// LogLevel used for all traffic-agent logging
	LogLevel string `json:"logLevel,omitzero"`

	// The name of the workload that the pod originates from
	WorkloadName string `json:"workloadName,omitzero"`

	// The kind of workload that the pod originates from
	WorkloadKind string `json:"workloadKind,omitzero"`

	// The host used when connecting to the traffic-manager
	ManagerHost string `json:"managerHost,omitzero"`

	// The port used when connecting to the traffic manager
	ManagerPort uint16 `json:"managerPort,omitzero"`

	// The port used by the agents restFUL API server
	APIPort uint16 `json:"apiPort,omitzero"`

	// The port used by the agent's GRPC tracing server
	TracingPort uint16 `json:"tracingPort,omitzero"`

	// Resources for the sidecar
	Resources *core.ResourceRequirements `json:"resources,omitempty"`

	// InitResources is the resource requirements for the initContainer sidecar
	InitResources *core.ResourceRequirements `json:"initResources,omitempty"`

	// The intercepts managed by the agent
	Containers []*Container `json:"containers,omitempty"`

	// SecurityContext for the sidecar
	SecurityContext *core.SecurityContext `json:"securityContext,omitempty"`
}

func (s *Sidecar) AgentConfig() *Sidecar {
	return s
}

// Marshal returns YAML encoding of the Sidecar.
func (s *Sidecar) Marshal() ([]byte, error) {
	return yaml.Marshal(s)
}

// SidecarExt must be implemented by a struct that can represent itself
// as YAML.
type SidecarExt interface {
	AgentConfig() *Sidecar

	Marshal() ([]byte, error)

	RecordInSpan(span trace.Span)
}

// SidecarType is Sidecar by default but can be any type implementing SidecarExt.
var SidecarType = reflect.TypeOf(Sidecar{}) //nolint:gochecknoglobals // extension point

// UnmarshalYAML creates a new instance of the SidecarType from the given YAML data.
func UnmarshalYAML(data []byte) (SidecarExt, error) {
	into := reflect.New(SidecarType).Interface()
	if err := yaml.Unmarshal(data, into); err != nil {
		return nil, err
	}
	return into.(SidecarExt), nil
}

func (s *Sidecar) RecordInSpan(span trace.Span) {
	bytes, err := yaml.Marshal(s)
	if err != nil {
		span.AddEvent("tel2.agent-sidecar-marshal-fail", trace.WithAttributes(
			attribute.String("tel2.agent-name", s.AgentName),
		))
		return
	}
	span.SetAttributes(
		attribute.String("tel2.agent-sidecar", string(bytes)),
	)
}
