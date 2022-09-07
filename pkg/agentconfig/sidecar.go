package agentconfig

import (
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

	DomainPrefix                   = "telepresence.getambassador.io/"
	InjectAnnotation               = DomainPrefix + "inject-" + ContainerName
	TerminatingTLSSecretAnnotation = DomainPrefix + "inject-terminating-tls-secret"
	OriginatingTLSSecretAnnotation = DomainPrefix + "inject-originating-tls-secret"
)

// Intercept describes the mapping between a service port and an intercepted container port.
type Intercept struct {
	// The name of the intercepted container port
	ContainerPortName string `json:"containerPortName,omitempty"`

	// Name of intercepted service
	ServiceName string `json:"serviceName,omitempty"`

	// UID of intercepted service
	ServiceUID types.UID `json:"serviceUID,omitempty"`

	// Name of intercepted service port
	ServicePortName string `json:"servicePortName,omitempty"`

	// TargetPortNumeric is set to true unless the servicePort has a symbolic target port
	TargetPortNumeric bool `json:"targetPortNumeric,omitempty"`

	// L4 protocol used by the intercepted port
	Protocol core.Protocol `json:"protocol,omitempty"`

	// L7 protocol used by the intercepted port
	AppProtocol string `json:"appProtocol,omitempty"`

	// True if the service is headless
	Headless bool `json:"headless,omitempty"`

	// The number of the intercepted container port
	ContainerPort uint16 `json:"containerPort,omitempty"`

	// Number of intercepted service port
	ServicePort uint16 `json:"servicePort,omitempty"`

	// The port number that the agent listens to
	AgentPort uint16 `json:"agentPort,omitempty"`
}

// Container describes one container that can have one or several intercepts.
type Container struct {
	// Name of the intercepted container
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// The intercepts managed by the agent
	Intercepts []*Intercept `json:"intercepts,omitempty"`

	// Prefix used for all keys in the container environment copy
	EnvPrefix string `json:"envPrefix,omitempty"`

	// Where the agent mounts the agents volumes
	MountPoint string `json:"mountPoint,omitempty"`

	// Mounts are the actual mount points that are mounted by this container
	Mounts []string
}

// The Sidecar configures the traffic-agent sidecar.
type Sidecar struct {
	// If Create is true, then this Config has not yet been filled in.
	Create bool `json:"create,omitempty"`

	// If Manual is true, then this Config is created manually
	Manual bool `json:"manual,omitempty"`

	// The fully qualified name of the traffic-agent image, i.e. "docker.io/tel2:2.5.4"
	AgentImage string `json:"agentImage,omitempty"`

	// The name of the traffic-agent instance. Typically, the same as the name of the workload owner
	AgentName string `json:"agentName,omitempty"`

	// The namespace of the intercepted pod
	Namespace string `json:"namespace,omitempty"`

	// LogLevel used for all traffic-agent logging
	LogLevel string `json:"logLevel,omitempty"`

	// The name of the workload that the pod originates from
	WorkloadName string `json:"workloadName,omitempty"`

	// The kind of workload that the pod originates from
	WorkloadKind string `json:"workloadKind,omitempty"`

	// The host used when connecting to the traffic-manager
	ManagerHost string `json:"managerHost,omitempty"`

	// The port used when connecting to the traffic manager
	ManagerPort uint16 `json:"managerPort,omitempty"`

	// The port used by the agents restFUL API server
	APIPort uint16 `json:"apiPort,omitempty"`

	// The port used by the agent's GRPC tracing server
	TracingPort uint16 `json:"tracingPort,omitempty"`

	// LogLevel used by the envoy instance
	EnvoyLogLevel string

	// The port used by the Envoy server
	EnvoyServerPort uint16

	// The port used for Envoy administration
	EnvoyAdminPort uint16

	// Resources for the sidecar
	Resources *core.ResourceRequirements `json:"resources,omitempty"`

	// InitResources is the resource requirements for the initContainer sidecar
	InitResources *core.ResourceRequirements `json:"initResources,omitempty"`

	// The intercepts managed by the agent
	Containers []*Container `json:"containers,omitempty"`
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
