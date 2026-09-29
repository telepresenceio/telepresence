package helm

import (
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Values mirrors the telepresence-oss chart's values.schema.yaml, field for field.
// A zero-valued block, scalar pointer, slice, or map means the key is unset;
// ToMap and ToUnstructured omit it accordingly.
type Values struct {
	Affinity                     core.Affinity             `json:"affinity,omitzero"`
	Agent                        Agent                     `json:"agent,omitzero"`
	AgentInjector                AgentInjector             `json:"agentInjector,omitzero"`
	APIPort                      *int32                    `json:"apiPort,omitzero"`
	Client                       Client                    `json:"client,omitzero"`
	ClientRbac                   ClientRbac                `json:"clientRbac,omitzero"`
	Compatibility                Compatibility             `json:"compatibility,omitzero"`
	ExternalEndpoint             ExternalEndpoint          `json:"externalEndpoint,omitzero"`
	ExtraEnv                     []core.EnvVar             `json:"extraEnv,omitzero"`
	ExtraVolumeMounts            []core.VolumeMount        `json:"extraVolumeMounts,omitzero"`
	ExtraVolumes                 []core.Volume             `json:"extraVolumes,omitzero"`
	Global                       map[string]string         `json:"global,omitzero"`
	Grpc                         Grpc                      `json:"grpc,omitzero"`
	Hooks                        Hooks                     `json:"hooks,omitzero"`
	HostNetwork                  *bool                     `json:"hostNetwork,omitzero"`
	Image                        Image                     `json:"image,omitzero"`
	Usage                        Usage                     `json:"usage,omitzero"`
	Intercept                    Intercept                 `json:"intercept,omitzero"`
	IsCI                         *bool                     `json:"isCI,omitzero"`
	Kafka                        Kafka                     `json:"kafka,omitzero"`
	Labels                       map[string]string         `json:"labels,omitzero"`
	LivenessProbe                core.Probe                `json:"livenessProbe,omitzero"`
	LogLevel                     *string                   `json:"logLevel,omitzero"`
	LogStreaming                 LogStreaming              `json:"logStreaming,omitzero"`
	ManagerRbac                  ManagerRbac               `json:"managerRbac,omitzero"`
	MaxNamespaceSpecificWatchers *int32                    `json:"maxNamespaceSpecificWatchers,omitzero"`
	NameOverride                 *string                   `json:"nameOverride,omitzero"`
	Namespaces                   []string                  `json:"namespaces,omitzero"`
	NamespaceSelector            meta.LabelSelector        `json:"namespaceSelector,omitzero"`
	NodeAgent                    NodeAgent                 `json:"nodeAgent,omitzero"`
	NodeSelector                 map[string]string         `json:"nodeSelector,omitzero"`
	PodAnnotations               map[string]string         `json:"podAnnotations,omitzero"`
	PodCIDRs                     []string                  `json:"podCIDRs,omitzero"`
	PodCIDRStrategy              *string                   `json:"podCIDRStrategy,omitzero"`
	PodLabels                    map[string]string         `json:"podLabels,omitzero"`
	PodSecurityContext           core.PodSecurityContext   `json:"podSecurityContext,omitzero"`
	PriorityClassName            *string                   `json:"priorityClassName,omitzero"`
	Prometheus                   Prometheus                `json:"prometheus,omitzero"`
	QuicTunnel                   QuicTunnel                `json:"quicTunnel,omitzero"`
	Rbac                         Rbac                      `json:"rbac,omitzero"`
	ReadinessProbe               core.Probe                `json:"readinessProbe,omitzero"`
	ReplicaCount                 *int32                    `json:"replicaCount,omitzero"`
	Resources                    core.ResourceRequirements `json:"resources,omitzero"`
	RouteController              RouteController           `json:"routeController,omitzero"`
	SchedulerName                *string                   `json:"schedulerName,omitzero"`
	Security                     Security                  `json:"security,omitzero"`
	SecurityContext              core.SecurityContext      `json:"securityContext,omitzero"`
	Service                      Service                   `json:"service,omitzero"`
	StartupProbe                 core.Probe                `json:"startupProbe,omitzero"`
	TelepresenceAPI              TelepresenceAPI           `json:"telepresenceAPI,omitzero"`
	Timeouts                     Timeouts                  `json:"timeouts,omitzero"`
	Tolerations                  []core.Toleration         `json:"tolerations,omitzero"`
	Workloads                    Workloads                 `json:"workloads,omitzero"`
}

// Agent configures the injected traffic-agent container.
type Agent struct {
	EnableConsumptionMetrics *bool                     `json:"enableConsumptionMetrics,omitzero"`
	EnableH2cProbing         *bool                     `json:"enableH2cProbing,omitzero"`
	InitContainer            AgentInitContainer        `json:"initContainer,omitzero"`
	Image                    AgentImage                `json:"image,omitzero"`
	InitResources            core.ResourceRequirements `json:"initResources,omitzero"`
	InitSecurityContext      core.SecurityContext      `json:"initSecurityContext,omitzero"`
	LogLevel                 *string                   `json:"logLevel,omitzero"`
	MaxIdleTime              *string                   `json:"maxIdleTime,omitzero"`
	MountPolicies            map[string]string         `json:"mountPolicies,omitzero"`
	Port                     *int32                    `json:"port,omitzero"`
	Resources                core.ResourceRequirements `json:"resources,omitzero"`
	SecurityContext          core.SecurityContext      `json:"securityContext,omitzero"`
	ServiceMesh              AgentServiceMesh          `json:"serviceMesh,omitzero"`
	WatchRetryInterval       *string                   `json:"watchRetryInterval,omitzero"`
}

// AgentInitContainer configures the init-container that runs before the traffic-agent.
type AgentInitContainer struct {
	Enabled *bool `json:"enabled,omitzero"`
}

// AgentImage overrides the injected traffic-agent's image.
type AgentImage struct {
	Name        *string                     `json:"name,omitzero"`
	PullPolicy  *string                     `json:"pullPolicy,omitzero"`
	PullSecrets []core.LocalObjectReference `json:"pullSecrets,omitzero"`
	Registry    *string                     `json:"registry,omitzero"`
	Tag         *string                     `json:"tag,omitzero"`
}

// AgentServiceMesh configures cooperation with a service mesh present in the pod.
type AgentServiceMesh struct {
	DialSubnets []string `json:"dialSubnets,omitzero"`
}

// AgentInjector configures the agent injector webhook service.
type AgentInjector struct {
	Certificate   AgentInjectorCertificate `json:"certificate,omitzero"`
	Enabled       *bool                    `json:"enabled,omitzero"`
	InjectPolicy  *string                  `json:"injectPolicy,omitzero"`
	MutationAware *bool                    `json:"mutationAware,omitzero"`
	Name          *string                  `json:"name,omitzero"`
	Secret        AgentInjectorSecret      `json:"secret,omitzero"`
	Service       AgentInjectorService     `json:"service,omitzero"`
	Webhook       AgentInjectorWebhook     `json:"webhook,omitzero"`
}

// AgentInjectorCertificate configures how the webhook's TLS certificate is obtained.
type AgentInjectorCertificate struct {
	AccessMethod *string                  `json:"accessMethod,omitzero"`
	Certmanager  AgentInjectorCertManager `json:"certmanager,omitzero"`
	Method       *string                  `json:"method,omitzero"`
	Regenerate   *bool                    `json:"regenerate,omitzero"`
	AltNames     []string                 `json:"altNames,omitzero"`
}

// AgentInjectorCertManager configures the cert-manager Certificate that backs the webhook certificate.
type AgentInjectorCertManager struct {
	CommonName *string   `json:"commonName,omitzero"`
	Duration   *string   `json:"duration,omitzero"`
	IssuerRef  IssuerRef `json:"issuerRef,omitzero"`
}

// IssuerRef names the cert-manager Issuer or ClusterIssuer to request a certificate from.
type IssuerRef struct {
	Group *string `json:"group,omitzero"`
	Kind  *string `json:"kind,omitzero"`
	Name  *string `json:"name,omitzero"`
}

// AgentInjectorSecret names the Secret the webhook's certificate is stored in.
type AgentInjectorSecret struct {
	Name *string `json:"name,omitzero"`
}

// AgentInjectorService configures the Service that fronts the agent-injector webhook.
type AgentInjectorService struct {
	Type     *string             `json:"type,omitzero"`
	Port     *intstr.IntOrString `json:"port,omitzero"`
	NodePort *intstr.IntOrString `json:"nodePort,omitzero"`
}

// AgentInjectorWebhook configures the MutatingWebhookConfiguration.
type AgentInjectorWebhook struct {
	AdmissionReviewVersions []string           `json:"admissionReviewVersions,omitzero"`
	FailurePolicy           *string            `json:"failurePolicy,omitzero"`
	Name                    *string            `json:"name,omitzero"`
	ObjectSelector          meta.LabelSelector `json:"objectSelector,omitzero"`
	Port                    *int32             `json:"port,omitzero"`
	ReinvocationPolicy      *string            `json:"reinvocationPolicy,omitzero"`
	ServicePath             *string            `json:"servicePath,omitzero"`
	SideEffects             *string            `json:"sideEffects,omitzero"`
	TimeoutSeconds          *int32             `json:"timeoutSeconds,omitzero"`
	URL                     *string            `json:"url,omitzero"`
}

// Client configures client.yaml, the config.yml settings delivered to
// connecting clients. It mirrors pkg/client's config struct section for
// section, since pkg/client's own unmarshaling is what actually reads it;
// durations, log levels, and addresses are strings, the shape a chart value takes.
type Client struct {
	Cluster       ClientCluster   `json:"cluster,omitzero"`
	ConnectionTTL *string         `json:"connectionTTL,omitzero"` // deprecated, use grpc.connectionTTL
	DNS           ClientDNS       `json:"dns,omitzero"`
	Docker        ClientDocker    `json:"docker,omitzero"`
	Grpc          ClientGrpc      `json:"grpc,omitzero"`
	Helm          ClientHelm      `json:"helm,omitzero"`
	Images        ClientImages    `json:"images,omitzero"`
	Intercept     ClientIntercept `json:"intercept,omitzero"`
	LogLevels     ClientLogLevels `json:"logLevels,omitzero"`
	Network       ClientNetwork   `json:"network,omitzero"` // windows-only client setting
	NodeAgent     ClientNodeAgent `json:"nodeAgent,omitzero"`
	Routing       ClientRouting   `json:"routing,omitzero"`
	Timeouts      ClientTimeouts  `json:"timeouts,omitzero"`
	Usage         ClientUsage     `json:"usage,omitzero"`
}

// ClientCluster configures the client's cluster-connection settings.
type ClientCluster struct {
	DefaultManagerNamespace *string  `json:"defaultManagerNamespace,omitzero"`
	MappedNamespaces        []string `json:"mappedNamespaces,omitzero"`
	ForceSPDY               *bool    `json:"forceSPDY,omitzero"`
	AgentPortForward        *bool    `json:"agentPortForward,omitzero"`
	ManagerAddress          *string  `json:"managerAddress,omitzero"`
	ManagerServerCA         *string  `json:"managerServerCA,omitzero"`
	VirtualIPSubnet         *string  `json:"virtualIPSubnet,omitzero"` // deprecated, use client.routing.virtualSubnet
}

// ClientDNS configures the client-side DNS resolver.
type ClientDNS struct {
	Error            *string             `json:"error,omitzero"`
	LocalIP          *string             `json:"localIP,omitzero"`      // deprecated, use localAddresses
	RemoteIP         *string             `json:"remoteIP,omitzero"`     // deprecated, use vifAddress
	LocalAddress     *string             `json:"localAddress,omitzero"` // deprecated, use localAddresses
	LocalAddresses   []string            `json:"localAddresses,omitzero"`
	VIFAddress       *string             `json:"vifAddress,omitzero"`
	IncludeSuffixes  []string            `json:"includeSuffixes,omitzero"`
	ExcludeSuffixes  []string            `json:"excludeSuffixes,omitzero"`
	Excludes         []string            `json:"excludes,omitzero"`
	Mappings         []*ClientDNSMapping `json:"mappings,omitzero"`
	LookupTimeout    *string             `json:"lookupTimeout,omitzero"`
	RecursionCheck   *bool               `json:"recursionCheck,omitzero"`
	UseComplexLookup *bool               `json:"useComplexLookup,omitzero"`
}

// ClientDNSMapping resolves Name to AliasFor instead of looking it up.
type ClientDNSMapping struct {
	Name     *string `json:"name,omitzero"`
	AliasFor *string `json:"aliasFor,omitzero"`
}

// ClientDocker configures the client's containerized-daemon settings.
type ClientDocker struct {
	AddHostGateway *bool             `json:"addHostGateway,omitzero"`
	Telemount      ClientDockerImage `json:"telemount,omitzero"`
	Teleroute      ClientDockerImage `json:"teleroute,omitzero"`
	HostGateway    *string           `json:"hostGateway,omitzero"`
	EnableIPv4     *bool             `json:"enableIPv4,omitzero"`
	EnableIPv6     *bool             `json:"enableIPv6,omitzero"`
}

// ClientDockerImage names an image used by a containerized-daemon sidecar.
type ClientDockerImage struct {
	RegistryAPI *string `json:"registryAPI,omitzero"`
	Registry    *string `json:"registry,omitzero"`
	Namespace   *string `json:"namespace,omitzero"`
	Repository  *string `json:"repository,omitzero"`
	Tag         *string `json:"tag,omitzero"`
}

// ClientGrpc configures the client's own gRPC transport settings, distinct
// from the top-level grpc block which configures the traffic-manager's.
type ClientGrpc struct {
	MaxReceiveSize     *resource.Quantity `json:"maxReceiveSize,omitzero"`
	DaemonPort         *uint16            `json:"daemonPort,omitzero"`
	TeleroutePort      *uint16            `json:"teleroutePort,omitzero"`
	SimulateDisconnect *string            `json:"simulateDisconnect,omitzero"`
	PingInterval       *string            `json:"pingInterval,omitzero"`
	WatchRetryInterval *string            `json:"watchRetryInterval,omitzero"`
}

// ClientHelm configures the client's own default chart source.
type ClientHelm struct {
	ChartURL *string `json:"chartURL,omitzero"`
}

// ClientImages configures the client's own image defaults, distinct from the
// top-level image block which configures the traffic-manager's.
type ClientImages struct {
	Registry        *string `json:"registry,omitzero"`
	AgentImage      *string `json:"agentImage,omitzero"`
	ClientImage     *string `json:"clientImage,omitzero"`
	WebhookRegistry *string `json:"webhookRegistry,omitzero"`
}

// ClientIntercept configures the client's intercept defaults.
type ClientIntercept struct {
	DefaultPort           *int    `json:"defaultPort,omitzero"`
	UseFtp                *bool   `json:"useFtp,omitzero"`
	LocalShortcut         *bool   `json:"localShortcut,omitzero"`
	LocalShortcutIsGlobal *bool   `json:"localShortcutIsGlobal,omitzero"`
	MountsRoot            *string `json:"mountsRoot,omitzero"`
	MountCompletionDelay  *string `json:"mountCompletionDelay,omitzero"`
	SshfsPath             *string `json:"sshfsPath,omitzero"`
}

// ClientLogLevels configures the log level of each client-side process.
type ClientLogLevels struct {
	CLI            *string `json:"cli,omitzero"`
	KubeAuthDaemon *string `json:"kubeAuthDaemon,omitzero"`
	UserDaemon     *string `json:"userDaemon,omitzero"`
	RootDaemon     *string `json:"rootDaemon,omitzero"`
}

// ClientNetwork configures windows-only client network settings.
type ClientNetwork struct {
	DNSWithFallback *bool `json:"dnsWithFallback,omitzero"`
}

// ClientNodeAgent configures the client's default node-agent attachment mode.
type ClientNodeAgent struct {
	Enabled *bool `json:"enabled,omitzero"`
}

// ClientRouting configures the subnets a client's virtual network interface proxies.
type ClientRouting struct {
	Subnets                 []string `json:"subnets,omitzero"`
	AlsoProxySubnets        []string `json:"alsoProxySubnets,omitzero"`
	NeverProxySubnets       []string `json:"neverProxySubnets,omitzero"`
	AllowConflictingSubnets []string `json:"allowConflictingSubnets,omitzero"`
	VirtualSubnet           *string  `json:"virtualSubnet,omitzero"`
	AutoResolveConflicts    *bool    `json:"autoResolveConflicts,omitzero"`
	UseTAP                  *bool    `json:"useTAP,omitzero"`

	// deprecated, use the Subnets field names above
	AlsoProxy        []string `json:"alsoProxy,omitzero"`
	NeverProxy       []string `json:"neverProxy,omitzero"`
	AllowConflicting []string `json:"allowConflicting,omitzero"`

	// deprecated, unused; the route-controller DaemonSet replaces these
	RecursionBlockDuration *string `json:"recursionBlockDuration,omitzero"`
	RecursionBlockTreads   *int    `json:"recursionBlockTreads,omitzero"`
}

// ClientTimeouts configures the client's operational timeouts.
type ClientTimeouts struct {
	ClusterConnect        *string `json:"clusterConnect,omitzero"`
	ConnectivityCheck     *string `json:"connectivityCheck,omitzero"`
	EndpointDial          *string `json:"endpointDial,omitzero"`
	Helm                  *string `json:"helm,omitzero"`
	Intercept             *string `json:"intercept,omitzero"`
	InterceptEndpointDial *string `json:"interceptEndpointDial,omitzero"`
	RoundtripLatency      *string `json:"roundtripLatency,omitzero"`
	ProxyDial             *string `json:"proxyDial,omitzero"`
	TrafficAgentConnect   *string `json:"trafficAgentConnect,omitzero"`
	TrafficManagerAPI     *string `json:"trafficManagerAPI,omitzero"`
	TrafficManagerConnect *string `json:"trafficManagerConnect,omitzero"`
	TrafficAgentArrival   *string `json:"trafficAgentArrival,omitzero"` // deprecated, use the chart's timeouts.agentArrival
	FtpReadWrite          *string `json:"ftpReadWrite,omitzero"`
	FtpShutdown           *string `json:"ftpShutdown,omitzero"`
	ContainerShutdown     *string `json:"containerShutdown,omitzero"`
}

// ClientUsage configures anonymous usage reporting from the client.
type ClientUsage struct {
	Enabled          *bool   `json:"enabled,omitzero"`
	CollectorAddress *string `json:"collectorAddress,omitzero"`
	Insecure         *bool   `json:"insecure,omitzero"`
}

// ClientRbac configures the RBAC resources rendered for non-admin users.
type ClientRbac struct {
	Create       *bool          `json:"create,omitzero"`
	LegacyAccess *bool          `json:"legacyAccess,omitzero"`
	Namespaces   []string       `json:"namespaces,omitzero"`
	RuleExtras   *bool          `json:"ruleExtras,omitzero"`
	Subjects     []rbac.Subject `json:"subjects,omitzero"`
}

// Compatibility makes the traffic-manager emulate an older version, for testing.
type Compatibility struct {
	Version *string `json:"version,omitzero"`
}

// ExternalEndpoint configures the opt-in external TLS gRPC listener.
type ExternalEndpoint struct {
	Enabled *bool           `json:"enabled,omitzero"`
	Port    *int32          `json:"port,omitzero"`
	Service ExternalService `json:"service,omitzero"`
	TLS     ExternalTLS     `json:"tls,omitzero"`
}

// ExternalService configures the Service that fronts the external TLS listener.
type ExternalService struct {
	Type        *string           `json:"type,omitzero"`
	Port        *int32            `json:"port,omitzero"`
	Annotations map[string]string `json:"annotations,omitzero"`
}

// ExternalTLS configures the certificate the external listener terminates with.
type ExternalTLS struct {
	SecretName  *string     `json:"secretName,omitzero"`
	CertManager CertManager `json:"certManager,omitzero"`
}

// CertManager has the chart provision the external listener's certificate via cert-manager.
type CertManager struct {
	Enabled   *bool     `json:"enabled,omitzero"`
	DNSNames  []string  `json:"dnsNames,omitzero"`
	IssuerRef IssuerRef `json:"issuerRef,omitzero"`
}

// Grpc configures the traffic-manager's and traffic-agent's gRPC transport.
type Grpc struct {
	ConnectionTTL  *string            `json:"connectionTTL,omitzero"`
	MaxReceiveSize *resource.Quantity `json:"maxReceiveSize,omitzero"`
}

// Hooks configures the images and pod settings used by the chart's install/upgrade hooks.
type Hooks struct {
	Busybox            HooksImage                `json:"busybox,omitzero"`
	Curl               HooksImage                `json:"curl,omitzero"`
	PodSecurityContext core.PodSecurityContext   `json:"podSecurityContext,omitzero"`
	Resources          core.ResourceRequirements `json:"resources,omitzero"`
	SecurityContext    core.SecurityContext      `json:"securityContext,omitzero"`
}

// HooksImage names a hook's image and how to pull it.
type HooksImage struct {
	Image            *string                     `json:"image,omitzero"`
	ImagePullSecrets []core.LocalObjectReference `json:"imagePullSecrets,omitzero"`
	PullPolicy       *string                     `json:"pullPolicy,omitzero"`
	Registry         *string                     `json:"registry,omitzero"`
	Tag              *string                     `json:"tag,omitzero"`
}

// Image configures the traffic-manager's own image.
type Image struct {
	ImagePullSecrets []core.LocalObjectReference `json:"imagePullSecrets,omitzero"`
	Name             *string                     `json:"name,omitzero"`
	PullPolicy       *string                     `json:"pullPolicy,omitzero"`
	Registry         *string                     `json:"registry,omitzero"`
	Tag              *string                     `json:"tag,omitzero"`
}

// Usage configures anonymous usage reporting from the traffic-manager.
type Usage struct {
	Enabled          *bool   `json:"enabled,omitzero"`
	CollectorAddress *string `json:"collectorAddress,omitzero"`
	Insecure         *bool   `json:"insecure,omitzero"`
}

// Intercept configures the traffic-manager's intercept policy.
type Intercept struct {
	AllowGlobalIntercepts *bool                `json:"allowGlobalIntercepts,omitzero"`
	Environment           InterceptEnvironment `json:"environment,omitzero"`
	InactiveBlockTimeout  *string              `json:"inactiveBlockTimeout,omitzero"`
}

// InterceptEnvironment configures which environment variables are withheld from clients.
type InterceptEnvironment struct {
	Excluded []string `json:"excluded,omitzero"`
}

// Kafka configures the optional Kafka personal-intercept provider.
type Kafka struct {
	Enabled   *bool                     `json:"enabled,omitzero"`
	Replicas  *int32                    `json:"replicas,omitzero"`
	Image     KafkaImage                `json:"image,omitzero"`
	Webhook   KafkaWebhook              `json:"webhook,omitzero"`
	Resources core.ResourceRequirements `json:"resources,omitzero"`
}

// KafkaImage names the Kafka provider image; empty registry and pull policy inherit image.*.
type KafkaImage struct {
	Name       *string `json:"name,omitzero"`
	PullPolicy *string `json:"pullPolicy,omitzero"`
	Registry   *string `json:"registry,omitzero"`
}

// KafkaWebhook configures the Kafka admission webhook.
type KafkaWebhook struct {
	FailurePolicy  *string `json:"failurePolicy,omitzero"`
	Port           *int32  `json:"port,omitzero"`
	TimeoutSeconds *int32  `json:"timeoutSeconds,omitzero"`
}

// LogStreaming bounds the traffic-manager's StreamLogs RPC.
type LogStreaming struct {
	ChunkSize      *resource.Quantity `json:"chunkSize,omitzero"`
	PodConcurrency *int32             `json:"podConcurrency,omitzero"`
	PodByteLimit   *resource.Quantity `json:"podByteLimit,omitzero"`
	Deadline       *string            `json:"deadline,omitzero"`
}

// ManagerRbac configures the RBAC resources rendered for the traffic-manager itself.
type ManagerRbac struct {
	Create     *bool    `json:"create,omitzero"`
	Namespaces []string `json:"namespaces,omitzero"`
}

// NodeAgent configures node-hosted traffic-agent mode.
type NodeAgent struct {
	Enabled   *bool   `json:"enabled,omitzero"`
	CriSocket *string `json:"criSocket,omitzero"`
}

// Prometheus configures the traffic-manager's optional metrics server.
type Prometheus struct {
	Port            *int32 `json:"port,omitzero"`
	DropClientLabel *bool  `json:"dropClientLabel,omitzero"`
}

// QuicTunnel configures the opt-in QUIC tunnel endpoint.
type QuicTunnel struct {
	Enabled      *bool               `json:"enabled,omitzero"`
	Port         *int32              `json:"port,omitzero"`
	ExternalHost *string             `json:"externalHost,omitzero"`
	ExternalPort *int32              `json:"externalPort,omitzero"`
	AgentPort    *int32              `json:"agentPort,omitzero"`
	Service      QuicTunnelService   `json:"service,omitzero"`
	Forwarder    QuicTunnelForwarder `json:"forwarder,omitzero"`
}

// QuicTunnelService configures the Service that fronts the QUIC endpoint.
type QuicTunnelService struct {
	Create      *bool             `json:"create,omitzero"`
	Type        *string           `json:"type,omitzero"`
	NodePort    *int32            `json:"nodePort,omitzero"`
	Annotations map[string]string `json:"annotations,omitzero"`
}

// QuicTunnelForwarder configures the quic-forwarder Deployment.
type QuicTunnelForwarder struct {
	PodLabels map[string]string         `json:"podLabels,omitzero"`
	Replicas  *int32                    `json:"replicas,omitzero"`
	Resources core.ResourceRequirements `json:"resources,omitzero"`
}

// Rbac controls whether only RBAC resources are rendered, omitting the traffic-manager.
type Rbac struct {
	Only *bool `json:"only,omitzero"`
}

// RouteController configures the route-controller DaemonSet.
type RouteController struct {
	Enabled      *bool                `json:"enabled,omitzero"`
	Image        RouteControllerImage `json:"image,omitzero"`
	LogLevel     *string              `json:"logLevel,omitzero"`
	ServiceCIDRs []string             `json:"serviceCIDRs,omitzero"`
}

// RouteControllerImage names the route-controller image.
type RouteControllerImage struct {
	Name       *string `json:"name,omitzero"`
	PullPolicy *string `json:"pullPolicy,omitzero"`
	Registry   *string `json:"registry,omitzero"`
}

// Security configures the traffic-manager's caller-authentication behavior.
type Security struct {
	Authentication Authentication `json:"authentication,omitzero"`
	Authorization  Authorization  `json:"authorization,omitzero"`
}

// Authentication configures how the traffic-manager authenticates its callers.
type Authentication struct {
	Mode *string `json:"mode,omitzero"`
	X509 X509    `json:"x509,omitzero"`
}

// X509 configures client-certificate authentication.
type X509 struct {
	Enabled *bool  `json:"enabled,omitzero"`
	Port    *int32 `json:"port,omitzero"`
}

// Authorization configures which grant satisfies the traffic-manager's authorization review.
type Authorization struct {
	RequiredGrant *string `json:"requiredGrant,omitzero"`
}

// Service configures the traffic-manager's own Service.
type Service struct {
	Type *string `json:"type,omitzero"`
}

// TelepresenceAPI configures the agent's local Telepresence API server.
type TelepresenceAPI struct {
	Port *int32 `json:"port,omitzero"`
}

// Timeouts configures the traffic-manager's operational timeouts.
type Timeouts struct {
	AgentArrival *string `json:"agentArrival,omitzero"`
}

// Workloads controls which workload kinds are recognized by Telepresence.
type Workloads struct {
	ArgoRollouts WorkloadEnabled `json:"argoRollouts,omitzero"`
	Deployments  WorkloadEnabled `json:"deployments,omitzero"`
	ReplicaSets  WorkloadEnabled `json:"replicaSets,omitzero"`
	StatefulSets WorkloadEnabled `json:"statefulSets,omitzero"`
}

// WorkloadEnabled toggles support for one workload kind.
type WorkloadEnabled struct {
	Enabled *bool `json:"enabled,omitzero"`
}
