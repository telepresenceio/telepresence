package agentconfig

import (
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// ConfigMap is the name of the ConfigMap that contains the agent configs
	ConfigMap = "telepresence-agents"

	ContainerName        = "traffic-agent"
	InitContainerName    = "tel-agent-init"
	AnnotationVolumeName = "traffic-annotations"
	AnnotationMountPoint = "/tel_pod_info"
	ConfigVolumeName     = "traffic-config"
	ConfigMountPoint     = "/etc/traffic-agent"
	ConfigFile           = "config.yaml"
	MountPrefixApp       = "/tel_app_mounts"
	ExportsVolumeName    = "export-volume"
	ExportsMountPoint    = "/tel_app_exports"
	TempVolumeName       = "tel-agent-tmp"
	TempMountPoint       = "/tmp"
	EnvPrefix            = "_TEL_"
	EnvPrefixAgent       = EnvPrefix + "AGENT_"
	EnvPrefixApp         = EnvPrefix + "APP_"

	// EnvInterceptContainer intercepted container propagated to client during intercept
	EnvInterceptContainer = "TELEPRESENCE_CONTAINER"

	// EnvInterceptMounts mount points propagated to client during intercept
	EnvInterceptMounts = "TELEPRESENCE_MOUNTS"

	// EnvAPIPort is the port number of the Telepresence API server, when it is enabled
	EnvAPIPort = "TELEPRESENCE_API_PORT"

	DomainPrefix     = "telepresence.getambassador.io/"
	InjectAnnotation = DomainPrefix + "inject-" + ContainerName
)

// Intercept describes the mapping between a service port and an intercepted container port
type Intercept struct {
	// The name of the intercepted container port
	ContainerPortName string `json:"containerPortName,omitempty" yaml:"containerPortName,omitempty"`

	// Name of intercepted service
	ServiceName string `json:"serviceName,omitempty" yaml:"serviceName,omitempty"`

	// UID of intercepted service
	ServiceUID types.UID `json:"serviceUID,omitempty" yaml:"serviceUID,omitempty"`

	// Name of intercepted service port
	ServicePortName string `json:"servicePortName,omitempty" yaml:"servicePortName,omitempty"`

	// TargetPortNumeric is set to true unless the servicePort has a symbolic target port
	TargetPortNumeric bool `json:"targetPortNumeric,omitempty" yaml:"targetPortNumeric,omitempty"`

	// L4 protocol used by the intercepted port
	Protocol core.Protocol `json:"protocol,omitempty" yaml:"protocol,omitempty"`

	// L7 protocol used by the intercepted port
	AppProtocol string `json:"appProtocol,omitempty" yaml:"appProtocol,omitempty"`

	// True if the service is headless
	Headless bool `json:"headless,omitempty" yaml:"headless,omitempty"`

	// The number of the intercepted container port
	ContainerPort uint16 `json:"containerPort,omitempty" yaml:"containerPort,omitempty"`

	// Number of intercepted service port
	ServicePort uint16 `json:"servicePort,omitempty" yaml:"servicePort,omitempty"`

	// The port number that the agent listens to
	AgentPort uint16 `json:"agentPort,omitempty" yaml:"agentPort,omitempty"`
}

// Container describes one container that can have one or several intercepts
type Container struct {
	// Name of the intercepted container
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// The intercepts managed by the agent
	Intercepts []*Intercept `json:"intercepts,omitempty" yaml:"intercepts,omitempty"`

	// Prefix used for all keys in the container environment copy
	EnvPrefix string `json:"envPrefix,omitempty" yaml:"envPrefix,omitempty"`

	// Where the agent mounts the agents volumes
	MountPoint string `json:"mountPoint,omitempty" yaml:"mountPoint,omitempty"`

	// Mounts are the actual mount points that are mounted by this container
	Mounts []string
}

// The Sidecar configures the traffic-agent sidecar
type Sidecar struct {
	// If Create is true, then this Config has not yet been filled in.
	Create bool `json:"create,omitempty" yaml:"create,omitempty"`

	// If Manual is true, then this Config is created manually
	Manual bool `json:"manual,omitempty" yaml:"manual,omitempty"`

	// The fully qualified name of the traffic-agent image, i.e. "docker.io/tel2:2.5.4"
	AgentImage string `json:"agentImage,omitempty" yaml:"agentImage,omitempty"`

	// The name of the traffic-agent instance. Typically, the same as the name of the workload owner
	AgentName string `json:"agentName,omitempty" yaml:"agentName,omitempty"`

	// The namespace of the intercepted pod
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`

	// LogLevel used for all traffic-agent logging
	LogLevel string `json:"logLevel,omitempty" yaml:"logLevel,omitempty"`

	// The name of the workload that the pod originates from
	WorkloadName string `json:"workloadName,omitempty" yaml:"workloadName,omitempty"`

	// The kind of workload that the pod originates from
	WorkloadKind string `json:"workloadKind,omitempty" yaml:"workloadKind,omitempty"`

	// The host used when connecting to the traffic-manager
	ManagerHost string `json:"managerHost,omitempty" yaml:"managerHost,omitempty"`

	// The port used when connecting to the traffic manager
	ManagerPort int32 `json:"managerPort,omitempty" yaml:"managerPort,omitempty"`

	// The port used by the agents restFUL API server
	APIPort uint16 `json:"apiPort,omitempty" yaml:"apiPort,omitempty"`

	// The port used by the agent's GRPC tracing server
	TracingPort uint16 `json:"tracingPort,omitempty" yaml:"tracingPort,omitempty"`

	// The intercepts managed by the agent
	Containers []*Container `json:"containers,omitempty" yaml:"containers,omitempty"`
}
