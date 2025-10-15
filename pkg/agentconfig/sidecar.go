package agentconfig

import (
	"time"

	core "k8s.io/api/core/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

const (
	ContainerName           = "traffic-agent"
	ManagerAppName          = "traffic-manager"
	InitContainerName       = "tel-agent-init"
	MountPrefixApp          = "/tel_app_mounts"
	ExportsVolumeName       = "export-volume"
	ExportsMountPoint       = "/tel_app_exports"
	TempVolumeName          = "tel-agent-tmp"
	TempMountPoint          = "/tmp"
	EnvPrefix               = "_TEL_"
	EnvPrefixAgent          = EnvPrefix + "AGENT_"
	EnvPrefixApp            = EnvPrefix + "APP_"
	PodInfoVolumeName       = "pod-info"
	PodInfoMountPath        = "/etc/podinfo"
	DownstreamTLSVolumeName = "downstream-tls"
	DownstreamTLSVolumePath = "/downstream-tls"
	UpstreamTLSVolumeName   = "upstream-tls"
	UpstreamTLSVolumePath   = "/upstream-tls"

	// EnvAgentConfig is the environment variable where the traffic-agent finds its own config.
	EnvAgentConfig = "AGENT_CONFIG"

	// EnvInterceptContainer intercepted container propagated to client during intercept.
	EnvInterceptContainer = "TELEPRESENCE_CONTAINER"

	// EnvInterceptMounts mount points propagated to client during intercept.
	EnvInterceptMounts = "TELEPRESENCE_MOUNTS"

	// EnvLocalMounts mount points that the client should mount locally (e.g. /tmp).
	EnvLocalMounts = "TELEPRESENCE_LOCAL_MOUNTS"

	// EnvAPIHost is the host name of the Telepresence API server when it is enabled.
	EnvAPIHost = "TELEPRESENCE_API_HOST"

	// EnvAPIPort is the port number of the Telepresence API server when it is enabled.
	EnvAPIPort = "TELEPRESENCE_API_PORT"

	WorkloadNameLabel    = annotation.DomainPrefix + "workloadName"
	WorkloadKindLabel    = annotation.DomainPrefix + "workloadKind"
	WorkloadEnabledLabel = annotation.DomainPrefix + "workloadEnabled"
)

type ReplacePolicy int

const (
	// ReplacePolicyIntercept The traffic-agent will receive all traffic intended for the ports of the app-container and
	// then either route that traffic to the client or to the original app-container depending on if the port is
	// intercepted or not. This will require an init-container when the targetPort of the service is numeric or
	// when the service is headless.
	ReplacePolicyIntercept ReplacePolicy = iota

	// ReplacePolicyContainer The traffic-agent is currently replacing the app container and routes all traffic to the
	// client.
	ReplacePolicyContainer

	// ReplacePolicyInactive The traffic-agent is not interfering with any ports or containers.
	ReplacePolicyInactive
)

// Intercept describes the mapping between a service port and an intercepted container port or, when
// service is used, just the container port.
type Intercept struct {
	// The name of the intercepted container port
	ContainerPortName string `json:"containerPortName,omitzero"`

	// Name of intercepted service
	ServiceName string `json:"serviceName,omitzero"`

	// UID of intercepted service
	ServiceUID k8sTypes.UID `json:"serviceUID,omitzero"`

	// Name of intercepted service port
	ServicePortName string `json:"servicePortName,omitzero"`

	// TargetPortNumeric is set to true unless the servicePort has a symbolic target port
	TargetPortNumeric bool `json:"targetPortNumeric,omitzero"`

	// L4 protocol used by the intercepted port
	Protocol types.Proto `json:"protocol,omitzero"`

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

	// Where the agent mounts its volumes
	MountPoint string `json:"mountPoint,omitzero"`

	// Mounts controls how the traffic-agent makes mounts available for this container. Each
	// policy is keyed with either the name of a volume or by a path prefix that matches the mounted
	// path.
	Mounts types.MountPolicies `json:"mounts,omitempty"`

	// MountPaths are the actual mount points that are mounted by this container
	// Deprecated: Use Mounts.
	MountPaths []string `json:"Mounts,omitempty"`

	// Replace is whether the agent should replace the intercepted container, it's ports, or nothing.
	Replace ReplacePolicy `json:"replace,omitzero"`
}

// The Sidecar configures the traffic-agent sidecar.
type Sidecar struct {
	// If Create is true, then this Config has not yet been filled in.
	Create bool `json:"create,omitzero"`

	// If Manual is true, then this Config is created manually.
	Manual bool `json:"manual,omitzero"`

	// The fully qualified name of the traffic-agent image, i.e. "ghcr.io/telepresenceio/tel2:2.5.4".
	AgentImage string `json:"agentImage,omitzero"`

	// One of "IfNotPresent", "Always", or "Never".
	PullPolicy string `json:"pullPolicy,omitzero"`

	// Secrets used when pulling the agent image from a private registry.
	PullSecrets []core.LocalObjectReference `json:"pullSecrets,omitempty"`

	// The name of the traffic-agent instance. Typically, the same as the name of the workload owner.
	AgentName string `json:"agentName,omitzero"`

	// The namespace of the intercepted pod.
	Namespace string `json:"namespace,omitzero"`

	// LogLevel used for all traffic-agent logging.
	LogLevel string `json:"logLevel,omitzero"`

	// The name of the workload that the pod originates from.
	WorkloadName string `json:"workloadName,omitzero"`

	// The kind of workload that the pod originates from.
	WorkloadKind k8sapi.Kind `json:"workloadKind,omitzero"`

	// The host used when connecting to the traffic-manager.
	ManagerHost string `json:"managerHost,omitzero"`

	// The port used when connecting to the traffic manager.
	ManagerPort uint16 `json:"managerPort,omitzero"`

	// The port used by the agents restFUL API server.
	APIPort uint16 `json:"apiPort,omitzero"`

	// Resources for the sidecar.
	Resources *core.ResourceRequirements `json:"resources,omitempty"`

	// InitResources is the resource requirements for the initContainer sidecar.
	InitResources *core.ResourceRequirements `json:"initResources,omitempty"`

	// MountPolicies controls how the agent will handle new mounts that might arrive when
	// the pod is created.
	MountPolicies types.MountPolicies `json:"mountPolicies,omitzero"`

	// The intercepts managed by the agent.
	Containers []*Container `json:"containers,omitempty"`

	// SecurityContext for the sidecar.
	SecurityContext *core.SecurityContext `json:"securityContext,omitempty"`

	// InitSecurityContext is the SecurityContext for the initContainer sidecar.
	InitSecurityContext *core.SecurityContext `json:"initSecurityContext,omitempty"`

	// ClientConnectionTTL is the maximum duration that the traffic-agent will keep an idle client connection alive.
	ClientConnectionTTL time.Duration `json:"clientConnectionTTL,omitempty,format:units"`
}

// InterceptTarget returns the container and intercepts that are parents of the given container port and protocol.
func (s *Sidecar) InterceptTarget(containerPort uint16, proto types.Proto) (*Container, InterceptTarget) {
	for _, c := range s.Containers {
		for i, ic := range c.Intercepts {
			if ic.ContainerPort == containerPort && ic.Protocol == proto {
				it := InterceptTarget{ic}
				i++
				if i < len(c.Intercepts) {
					for _, ic := range c.Intercepts[i:] {
						if ic.ContainerPort == containerPort && ic.Protocol == proto {
							it = append(it, ic)
						}
					}
				}
				return c, it
			}
		}
	}
	return nil, nil
}

// InterceptorInactivePort returns the port that the interceptor should write to when it isn't serving
// an intercept. The port will be the container port unless some service uses a numeric target port
// that targets the container port.
//
// When a numeric target port is specified, the init-container sets up an iptables NAT PREROUTING rule
// to redirect all traffic destined for the container port to the corresponding port where the agent's
// forwarder is listening. When no intercept is active, the forwarder routes traffic to the container
// port using the pod's IP address. However, directly routing to the container port would trigger the
// NAT PREROUTING rule again, causing an infinite loop. To avoid this, the forwarder uses a proxy port,
// which is redirected to the container port via an iptables NAT OUTPUT rule.
func (s *Sidecar) InterceptorInactivePort(containerPort uint16, proto types.Proto) uint16 {
	_, it := s.InterceptTarget(containerPort, proto)
	if it != nil && it.TargetPortNumeric() {
		return s.ProxyPort(it.AgentPort())
	}
	return containerPort
}

// Clone returns a deep copy of the Sidecar.
func (s *Sidecar) Clone() *Sidecar {
	cs := *s
	for ci, cn := range cs.Containers {
		ccn := *cn
		cs.Containers[ci] = &ccn
		for ii, ic := range ccn.Intercepts {
			cic := *ic
			ccn.Intercepts[ii] = &cic
		}
	}
	return &cs
}

// EachContainer will find each container and match it against a container
// in the pod using its name. The given function is called once for each match.
func (s *Sidecar) EachContainer(pod *core.Pod, f func(*core.Container, *Container)) {
	cns := pod.Spec.Containers
	for _, cc := range s.Containers {
		for i := range cns {
			if app := &cns[i]; app.Name == cc.Name {
				f(app, cc)
				break
			}
		}
	}
}

// Marshal returns YAML encoding of the Sidecar.
func (s *Sidecar) Marshal() ([]byte, error) {
	return yaml.Marshal(s)
}

// UnmarshalYAML creates a new instance of the SidecarType from the given YAML data.
func UnmarshalYAML(data []byte) (*Sidecar, error) {
	into := new(Sidecar)
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, into, true); err != nil {
		return nil, err
	}
	return into, nil
}

// MarshalTight marshals the given instance into JSON data, with data relating to the creation of the
// container manifest stripped off.
func MarshalTight(ac *Sidecar) (string, error) {
	// Strip things that are not needed once the container has been created.
	ai := ac.AgentImage
	pp := ac.PullPolicy
	ps := ac.PullSecrets
	ir := ac.InitResources
	sc := ac.SecurityContext
	is := ac.InitSecurityContext

	ac.AgentImage = ""
	ac.PullPolicy = ""
	ac.PullSecrets = nil
	ac.InitResources = nil
	ac.SecurityContext = nil
	ac.InitSecurityContext = nil

	data, err := json.Marshal(ac)
	ac.AgentImage = ai
	ac.PullPolicy = pp
	ac.PullSecrets = ps
	ac.InitResources = ir
	ac.SecurityContext = sc
	ac.InitSecurityContext = is

	if err != nil {
		return "", err
	}
	return string(data), err
}

// UnmarshalJSON creates a new instance of the SidecarType from the given JSON data.
func UnmarshalJSON(data string) (*Sidecar, error) {
	into := new(Sidecar)
	if err := json.Unmarshal([]byte(data), into, true); err != nil {
		return nil, err
	}
	return into, nil
}
