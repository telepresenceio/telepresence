package agentconfig

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"time"

	core "k8s.io/api/core/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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
	CoverVolumeName         = "tel-agent-cover"
	EnvPrefix               = "_TEL_"
	EnvPrefixAgent          = EnvPrefix + "AGENT_"
	EnvPrefixApp            = EnvPrefix + "APP_"
	PodInfoVolumeName       = "pod-info"
	PodInfoMountPath        = "/etc/podinfo"
	DownstreamTLSVolumeName = "downstream-tls"
	DownstreamTLSVolumePath = "/downstream-tls"
	UpstreamTLSVolumeName   = "upstream-tls"
	UpstreamTLSVolumePath   = "/upstream-tls"

	// ManagerTokenAudience is the audience of the ServiceAccount token that the
	// traffic-agent presents to the traffic-manager. Tokens bound to this audience
	// are not valid against the Kubernetes API server.
	ManagerTokenAudience = "traffic-manager"

	// ManagerTokenVolumeName is the projected volume holding the traffic-agent's
	// manager-audience ServiceAccount token.
	ManagerTokenVolumeName = "traffic-manager-token"

	// ManagerTokenMountPath is where ManagerTokenVolumeName is mounted in the
	// traffic-agent container. Deliberately outside /var/run/secrets, whose
	// subdirectories the agent exposes to the intercepting client.
	ManagerTokenMountPath = "/var/run/telepresence.io"

	// ManagerTokenFile is the name of the token file within ManagerTokenMountPath.
	ManagerTokenFile = "manager-token"

	// EnvAgentConfig is the environment variable where the traffic-agent finds its own config.
	EnvAgentConfig = "AGENT_CONFIG"

	// EnvAgentUID is the user ID that the traffic-agent runs as.
	EnvAgentUID = "AGENT_UID"

	// EnvAgentGID is the primary group ID that the traffic-agent runs as.
	EnvAgentGID = "AGENT_GID"

	// DefaultAgentGID is the default primary group ID for the traffic-agent container. The
	// group is what distinguishes the agent's traffic from the application's in the iptables
	// owner matches, so it must not be shared with an application container.
	DefaultAgentGID int64 = 7439

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

	// EnvAgentQuicPort is the UDP port the traffic-agent's own QUIC listener binds
	// to, set only when the traffic-manager that generated this config has its
	// QUIC tunnel enabled (see managerutil.Env.TunnelQuicAgentPort). Per "Agent
	// connections over QUIC" in docs/reference/quic-transport-architecture.md, the agent
	// fetches its server certificate for this listener over its authenticated
	// manager session (GetQuicAgentCert), for quicfwd.AgentSNI(its own pod UID).
	EnvAgentQuicPort = "AGENT_QUIC_PORT"

	// EnvNodeAgentContainerIDs holds a JSON object mapping agent container name to CRI
	// container ID, e.g. {"app":"containerd://abc"}. The traffic-manager sets it when it
	// creates a node-agent Job, and the node-agent reads it to resolve each configured
	// container's host PID through the CRI socket.
	EnvNodeAgentContainerIDs = "_TEL_NODE_AGENT_CONTAINER_IDS"

	// EnvNodeAgentCRISocket is the CRI unix socket path that the traffic-manager sets on a
	// node-agent Job when one is configured (Helm value nodeAgent.criSocket). When unset or
	// empty, the node-agent picks the well-known CRI socket that recognizes its target
	// containers with cri.SocketFor, under NodeAgentHostRunDir when that mount is present.
	EnvNodeAgentCRISocket = "_TEL_NODE_AGENT_CRI_SOCKET"

	// NodeAgentHostRunDir is where a node-agent Job mounts the node's /run directory when no
	// CRI socket path is configured, so that the agent can probe the well-known CRI sockets
	// beneath it.
	NodeAgentHostRunDir = "/host/run"

	// EnvNodeAgentPodIP carries the target pod's IP, set by the traffic-manager on a
	// node-agent Job. It is the PodIP of the netfilter ruleset the node-agent programs into
	// the target pod's network namespace.
	EnvNodeAgentPodIP = "_TEL_NODE_AGENT_POD_IP"

	WorkloadNameLabel    = annotation.DomainPrefix + "workloadName"
	WorkloadKindLabel    = annotation.DomainPrefix + "workloadKind"
	WorkloadEnabledLabel = annotation.DomainPrefix + "workloadEnabled"
)

// AgentGIDFromEnv returns the traffic-agent group id configured through
// EnvAgentGID. ok is false when the variable is unset or empty; err is
// non-nil when it is set but does not parse as a 32-bit unsigned integer.
func AgentGIDFromEnv() (gid uint32, ok bool, err error) {
	v, set := os.LookupEnv(EnvAgentGID)
	if !set || v == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, true, fmt.Errorf("invalid %s %q: %w", EnvAgentGID, v, err)
	}
	return uint32(parsed), true, nil
}

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

	// Port used to reach the application when no intercept matches, when non-zero.
	InactivePort uint16 `json:"inactivePort,omitzero"`

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
	//
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
	LogLevel slog.Level `json:"logLevel,omitzero"`

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

	// QuicPort is the UDP port the traffic-agent's own QUIC listener binds to.
	// Zero (the default) means the traffic-manager that generated this config
	// has no QUIC tunnel enabled, so the agent runs no QUIC listener at all.
	QuicPort uint16 `json:"quicPort,omitzero"`

	// Resources for the sidecar.
	Resources *core.ResourceRequirements `json:"resources,omitempty"`

	// InitResources is the resource requirements for the initContainer sidecar.
	InitResources *core.ResourceRequirements `json:"initResources,omitempty"`

	// MountPolicies controls how the agent will handle new mounts that might arrive when
	// the pod is created.
	MountPolicies types.MountPolicies `json:"mountPolicies,omitzero"`

	// MeshDialSubnets are subnets for which outbound connections made by the traffic-agent
	// (such as dials performed on behalf of a connected client) must pass through a
	// service-mesh proxy instead of bypassing it. Typically the mesh's virtual address
	// range for external services, e.g. Istio's ServiceEntry auto-allocation range
	// 240.240.0.0/16.
	MeshDialSubnets []netip.Prefix `json:"meshDialSubnets,omitempty"`

	// The intercepts managed by the agent.
	Containers []*Container `json:"containers,omitempty"`

	// SecurityContext for the sidecar.
	SecurityContext *core.SecurityContext `json:"securityContext,omitempty"`

	// InitSecurityContext is the SecurityContext for the initContainer sidecar.
	InitSecurityContext *core.SecurityContext `json:"initSecurityContext,omitempty"`

	// ClientConnectionTTL is the maximum duration that the traffic-agent will keep an idle client connection alive.
	ClientConnectionTTL time.Duration `json:"clientConnectionTTL,omitempty"`

	// EnableMetrics is true if the traffic-agent should send consumption reports to the traffic-manager.
	EnableMetrics bool `json:"enableMetrics,omitempty"`

	// EnableH2cProbing is true if the traffic-agent should enable H2C probing on TCP ports that have no TLS and no appProtocol.
	EnableH2cProbing bool `json:"enableH2cProbing,omitempty"`

	// WatchRetryInterval is the interval between retries that a watcher uses when the gRPC connection to the traffic-manager is lost.
	WatchRetryInterval time.Duration `json:"watchRetryInterval"`
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
// an intercept. An explicit inactive port takes precedence. Otherwise, the port will be the container
// port unless some service uses a numeric target port that targets the container port.
//
// When a numeric target port is specified, the init-container sets up an iptables NAT PREROUTING rule
// to redirect all traffic destined for the container port to the corresponding port where the agent's
// forwarder is listening. When no intercept is active, the forwarder routes traffic to the container
// port using the pod's IP address. However, directly routing to the container port would trigger the
// NAT PREROUTING rule again, causing an infinite loop. To avoid this, the forwarder uses a proxy port,
// which is redirected to the container port via an iptables NAT OUTPUT rule.
func (s *Sidecar) InterceptorInactivePort(containerPort uint16, proto types.Proto) uint16 {
	_, it := s.InterceptTarget(containerPort, proto)
	if it != nil {
		if inactivePort := it.InactivePort(); inactivePort != 0 {
			return inactivePort
		}
		if it.TargetPortNumeric() {
			return s.ProxyPort(it.AgentPort())
		}
	}
	return containerPort
}

// PassThroughTarget returns the address that a forwarder or protocol prober should
// dial to reach containerPort on the application when no intercept is redirecting the
// connection elsewhere. appPodIP is the IP of the pod that hosts the application
// containers (Config.AppPodIP: the agent's own pod for a sidecar, the target pod for a
// node-agent).
//
// The result mirrors the pod-IP redirect gate programmed by agentnft (see
// pkg/agentnft/ruleset.go, gate 2): when the pod carries the agent's nftables ruleset,
// traffic addressed to the pod IP's app ports is unconditionally redirected to the
// agent, including the agent's own traffic. A numeric target port resolves to a proxy
// port via InterceptorInactivePort, and dialing that proxy port at appPodIP is safe --
// the proxy-port DNAT rewrites it back to containerPort, breaking the loop the redirect
// would otherwise create. A named target port has no proxy port, so
// InterceptorInactivePort returns containerPort unchanged; dialing appPodIP there would
// hit the same unconditional redirect and loop back into the agent, so the address
// switches to the family-matched loopback instead, which gate 3 exempts.
//
// nftRedirects says whether that ruleset is present: always true for a node-agent, and
// NftRedirectsActive() for a sidecar. Without it there is no gate to loop through, and
// the named-port dial keeps the pod IP, which an application may be bound to
// exclusively; loopback would not reach it.
func (s *Sidecar) PassThroughTarget(appPodIP netip.Addr, containerPort uint16, proto types.Proto, nftRedirects bool) netip.AddrPort {
	cp := s.InterceptorInactivePort(containerPort, proto)
	targetIP := appPodIP
	if cp == containerPort && nftRedirects {
		targetIP = LoopbackFor(targetIP)
	}
	return netip.AddrPortFrom(targetIP, cp)
}

// NftRedirectsActive reports whether this config makes a sidecar's pod carry the
// agent's nftables ruleset: a headless or numeric-target intercept in a container
// that is replaced on intercept requires the init container to program it. Without
// the ruleset there is no pod-IP redirect gate, and the pass-through dial must use
// the pod IP so that an application that binds to it (rather than to a wildcard or
// loopback address) stays reachable. A node-agent programs the ruleset
// unconditionally, so this predicate only applies to the sidecar.
func (s *Sidecar) NftRedirectsActive() bool {
	for _, cc := range s.Containers {
		if cc.Replace == ReplacePolicyIntercept {
			for _, ic := range cc.Intercepts {
				if ic.Headless || ic.TargetPortNumeric {
					return true
				}
			}
		}
	}
	return false
}

// LoopbackFor returns the loopback address of the same address family as ip.
func LoopbackFor(ip netip.Addr) netip.Addr {
	if ip.Is6() {
		return netip.IPv6Loopback()
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
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

// FindIntercept finds the [Container] and [Intercept] configuration that matches the given service name, container name, and service- or container port.
// The port will be considered a service port for intercepts that have a service UID and a container port for service less intercepts.
func (s *Sidecar) FindIntercept(serviceName, containerName string, port types.PortIdentifier) (foundCN *Container, foundIC *Intercept, err error) {
	for _, cn := range s.Containers {
		for _, ic := range cn.Intercepts {
			if !(serviceName == "" || serviceName == ic.ServiceName) {
				continue
			}
			if port != "" {
				if ic.ServiceUID != "" {
					if !IsInterceptForService(port, ic) {
						continue
					}
				} else if !IsInterceptForContainer(port, ic) {
					continue
				}
			}
			if foundIC == nil {
				foundCN = cn
				if containerName != "" {
					for _, cx := range s.Containers {
						if cx.Name == containerName {
							foundCN = cx
							break
						}
					}
				}
				foundIC = ic
				continue
			}
			var msg string
			switch {
			case serviceName == "" && port == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable ports.\n"+
					"Please specify the service and/or port you want to intercept "+
					"by passing the --service=<svc> and/or --port=<local:portName/portNumber> flag.",
					s.WorkloadKind, s.WorkloadName, s.Namespace)
			case serviceName == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable services with port %s.\n"+
					"Please specify the service you want to intercept by passing the --service=<svc> flag.",
					s.WorkloadKind, s.WorkloadName, s.Namespace, port)
			case port == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable ports in service %s.\n"+
					"Please specify the port you want to intercept by passing the --port=<local:svcPortName> flag.",
					s.WorkloadKind, s.WorkloadName, s.Namespace, serviceName)
			default:
				msg = fmt.Sprintf("%s %s.%s intercept config is broken. Service %s, port %s is declared more than once\n",
					s.WorkloadKind, s.WorkloadName, s.Namespace, serviceName, port)
			}
			return nil, nil, errcat.User.New(msg)
		}
	}
	if foundIC != nil {
		return foundCN, foundIC, nil
	}

	ss := ""
	if serviceName != "" {
		if port != "" {
			ss = fmt.Sprintf(" matching service %s, port %s", serviceName, port)
		} else {
			ss = fmt.Sprintf(" matching service %s", serviceName)
		}
	} else if port != "" {
		ss = fmt.Sprintf(" matching port %s", port)
	}
	return nil, nil, errcat.User.Newf("%s %s.%s has no interceptable port%s", s.WorkloadKind, s.WorkloadName, s.Namespace, ss)
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
	// Sidecars are cached and reused by concurrent admission requests. Strip
	// creation-only fields from a value copy so marshaling never mutates that
	// shared config while another request is building an injected container.
	tight := *ac
	tight.AgentImage = ""
	tight.PullPolicy = ""
	tight.PullSecrets = nil
	tight.InitResources = nil
	tight.SecurityContext = nil
	tight.InitSecurityContext = nil

	data, err := json.Marshal(&tight)
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
