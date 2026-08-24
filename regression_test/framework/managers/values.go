package managers

import (
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

const (
	// ManagerNamespace is the stable namespace the single rtest traffic-manager
	// release lives in.
	ManagerNamespace = "rtest-manager"

	// TestServiceAccount is the ServiceAccount that clientRbac and managerRbac
	// grant access to, and the identity connections use with --as.
	TestServiceAccount = "telepresence-test-developer"

	// ManagedNamespaceLabel is the label key the namespaceSelector matches.
	// Namespaces the manager should manage carry this label.
	ManagedNamespaceLabel = "rtest.telepresence.io/managed"
)

// Values is the typed subset of the telepresence-oss chart values used by the
// manager catalog. Field names and JSON tags mirror charts/telepresence-oss's
// values.schema.yaml.
type Values struct {
	LogLevel      string        `json:"logLevel,omitempty"`
	Image         Image         `json:"image,omitzero"`
	Agent         AgentValues   `json:"agent,omitzero"`
	AgentInjector AgentInjector `json:"agentInjector,omitzero"`
	ClientRbac    Rbac          `json:"clientRbac,omitzero"`
	ManagerRbac   ManagerRbac   `json:"managerRbac,omitzero"`
	Timeouts      Timeouts      `json:"timeouts,omitzero"`
	// Resources is the chart's top-level resources shape: the
	// traffic-manager container's requests/limits.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeAgent is the chart's top-level nodeAgent.* shape (node-hosted
	// traffic-agent mode, cluster-wide). Not to be confused with Client's
	// nested client.nodeAgent.enabled, the client-side default served to
	// connecting clients.
	NodeAgent NodeAgentValues `json:"nodeAgent,omitzero"`
	// Client is the chart's client.* shape: cluster-served defaults handed
	// to connecting clients, restricted to the keys wave 3 suites configure.
	Client Client `json:"client,omitzero"`
	// QuicTunnel is the chart's quicTunnel.* shape, restricted to the keys
	// the catalog configures.
	QuicTunnel QuicTunnel `json:"quicTunnel,omitzero"`
	// Security is the chart's security.* shape: traffic-manager caller
	// authentication/authorization.
	Security Security `json:"security,omitzero"`
	// ExternalEndpoint is the chart's externalEndpoint.* shape: the opt-in
	// external TLS gRPC listener that lets a client connect without ever
	// contacting the Kubernetes API server.
	ExternalEndpoint ExternalEndpoint `json:"externalEndpoint,omitzero"`
	// Compatibility is the chart's compatibility.* shape: for testing only,
	// makes the manager emulate an older version (see checkCompat in
	// cmd/traffic/cmd/manager/service.go).
	Compatibility Compatibility `json:"compatibility,omitzero"`
	// TelepresenceAPI is the chart's telepresenceAPI.* shape.
	TelepresenceAPI TelepresenceAPI `json:"telepresenceAPI,omitzero"`
	// Workloads is the chart's workloads.* shape: whether each workload
	// kind is recognized by the traffic-manager and injector.
	Workloads Workloads `json:"workloads,omitzero"`
	// Intercept is the chart's intercept.* shape, restricted to the keys
	// the catalog configures.
	Intercept Intercept `json:"intercept,omitzero"`
	// Namespaces and NamespaceSelector are mutually exclusive per the chart
	// (values.schema.yaml's namespaces/namespaceSelector descriptions):
	// setting a static Namespaces list must null NamespaceSelector, and
	// Merge does so (see the Namespaces handling below).
	Namespaces        []string         `json:"namespaces,omitempty"`
	NamespaceSelector *labels.Selector `json:"namespaceSelector,omitempty"`
	// PodCIDRs is only meaningful when PodCIDRStrategy is "environment"
	// (values.schema.yaml's podCIDRs description).
	PodCIDRs        []string `json:"podCIDRs,omitempty"`
	PodCIDRStrategy string   `json:"podCIDRStrategy,omitempty"`
	// ExtraEnv/ExtraVolumes/ExtraVolumeMounts mirror the chart's own
	// extraEnv/extraVolumes/extraVolumeMounts passthrough values, applied to
	// the traffic-manager container/pod. cover.go appends to these in
	// coverage mode (RTEST_COVER=1) to mount the GOCOVERDIR hostPath.
	ExtraEnv          []corev1.EnvVar      `json:"extraEnv,omitempty"`
	ExtraVolumes      []corev1.Volume      `json:"extraVolumes,omitempty"`
	ExtraVolumeMounts []corev1.VolumeMount `json:"extraVolumeMounts,omitempty"`
	// Usage has no omit option: usage.enabled=false must always reach the
	// chart, whose own default is true.
	Usage Usage `json:"usage"`
}

// AgentInjector is the chart's agentInjector.* shape, restricted to the
// fields the catalog configures: whether the webhook is enabled at all, the
// injection policy, and the mutating webhook's certificate/reinvocation
// settings.
type AgentInjector struct {
	// Enabled is a pointer so InjectorDisabled() can force it to false: a
	// plain bool can't be distinguished from "not set" by Merge, which would
	// make agentInjector.enabled=false unreachable through the overlay.
	Enabled      *bool       `json:"enabled,omitempty"`
	InjectPolicy string      `json:"injectPolicy,omitempty"`
	Certificate  Certificate `json:"certificate,omitzero"`
	Webhook      Webhook     `json:"webhook,omitzero"`
}

// Certificate is the chart's agentInjector.certificate.* shape, restricted
// to the fields the catalog configures.
type Certificate struct {
	AccessMethod string `json:"accessMethod,omitempty"`
	Regenerate   bool   `json:"regenerate,omitempty"`
}

// Webhook is the chart's agentInjector.webhook.* shape, restricted to the
// field the catalog configures.
type Webhook struct {
	ReinvocationPolicy string `json:"reinvocationPolicy,omitempty"`
}

// Image is the chart's image.* and agent.image.* shape.
type Image struct {
	Registry   string `json:"registry,omitempty"`
	Name       string `json:"name,omitempty"`
	Tag        string `json:"tag,omitempty"`
	PullPolicy string `json:"pullPolicy,omitempty"`
}

// AgentValues is the chart's agent.* shape, restricted to the image settings
// the catalog configures.
type AgentValues struct {
	Image Image `json:"image,omitzero"`
	// EnableH2cProbing is a pointer so NodeAgent() can force it to false:
	// node-agent Jobs enter an existing pod's namespaces and have no
	// sidecar of their own to h2c-probe, so node-agent specs disable it
	// explicitly. A plain bool couldn't be distinguished from "not set" by
	// Merge.
	EnableH2cProbing *bool `json:"enableH2cProbing,omitempty"`
}

// NodeAgentValues is the chart's top-level nodeAgent.* shape, restricted to
// the enabled flag: node-hosted traffic-agent mode, cluster-wide.
type NodeAgentValues struct {
	Enabled bool `json:"enabled,omitempty"`
}

// Client is the chart's client.* shape: cluster-served config a connecting
// client starts from, restricted to the keys wave 3 suites configure.
// Unlike most of the chart's top-level objects, client.* is NOT
// additionalProperties:false in values.schema.yaml, so keys the schema
// doesn't spell out under properties (client.logLevels.*,
// client.routing.autoResolveConflicts) still validate; both are genuine
// pkg/client/config.go fields (LogLevels, Routing.AutoResolveConflicts) the
// manager pushes down to clients, mirrored here as typed fields rather than
// left to an untyped escape hatch.
type Client struct {
	NodeAgent ClientNodeAgent `json:"nodeAgent,omitzero"`
	LogLevels ClientLogLevels `json:"logLevels,omitzero"`
	Routing   ClientRouting   `json:"routing,omitzero"`
	DNS       ClientDNS       `json:"dns,omitzero"`
}

// ClientNodeAgent is the chart's client.nodeAgent.* shape: the cluster-wide
// default for a connecting client's --node-agent flag.
type ClientNodeAgent struct {
	Enabled bool `json:"enabled,omitempty"`
}

// ClientLogLevels is the chart's client.logLevels.* shape (open passthrough,
// not enumerated in values.schema.yaml's client.properties, but a real
// pkg/client/config.go LogLevels field): the cluster-served default log
// level for each daemon, expressed as one of the chart's logLevel enum
// strings ("error", "warning"/"warn", "info", "debug", "trace").
type ClientLogLevels struct {
	RootDaemon string `json:"rootDaemon,omitempty"`
	UserDaemon string `json:"userDaemon,omitempty"`
}

// ClientRouting is the chart's client.routing.* shape.
type ClientRouting struct {
	AlsoProxySubnets        []string `json:"alsoProxySubnets,omitempty"`
	NeverProxySubnets       []string `json:"neverProxySubnets,omitempty"`
	AllowConflictingSubnets []string `json:"allowConflictingSubnets,omitempty"`
	// AutoResolveConflicts is a pointer since its client-side default is
	// true (pkg/client/config.go's defaultAutoResolveConflicts): a plain
	// bool could not express "push down false" through Merge.
	AutoResolveConflicts *bool `json:"autoResolveConflicts,omitempty"`
}

// ClientDNS is the chart's client.dns.* shape, restricted to the key wave 3
// suites configure.
type ClientDNS struct {
	IncludeSuffixes []string `json:"includeSuffixes,omitempty"`
}

// QuicTunnel is the chart's quicTunnel.* shape, restricted to the keys the
// catalog configures: whether the listener is enabled, the Service
// front-ending it, and the externally advertised host/port (unset lets the
// traffic-manager self-discover both).
type QuicTunnel struct {
	Enabled      bool              `json:"enabled,omitempty"`
	Service      QuicTunnelService `json:"service,omitzero"`
	ExternalHost string            `json:"externalHost,omitempty"`
	ExternalPort int               `json:"externalPort,omitempty"`
}

// QuicTunnelService is the chart's quicTunnel.service.* shape, restricted to
// the keys the catalog configures.
type QuicTunnelService struct {
	Type     string `json:"type,omitempty"`
	NodePort int    `json:"nodePort,omitempty"`
}

// Security is the chart's security.* shape: traffic-manager caller
// authentication/authorization.
type Security struct {
	Authentication Authentication `json:"authentication,omitzero"`
	Authorization  Authorization  `json:"authorization,omitzero"`
}

// Authorization is the chart's security.authorization.* shape.
type Authorization struct {
	// RequiredGrant is one of "portforward", "telepresence", or "any" --
	// which grant a client must hold to pass the traffic-manager's
	// authorization review at connect and attach time (values.yaml's
	// security.authorization.requiredGrant).
	RequiredGrant string `json:"requiredGrant,omitempty"`
}

// Authentication is the chart's security.authentication.* shape.
type Authentication struct {
	Mode string `json:"mode,omitempty"`
	X509 X509   `json:"x509,omitzero"`
}

// X509 is the chart's security.authentication.x509.* shape, restricted to
// the enabled flag.
type X509 struct {
	// Enabled is a pointer so AuthEnforcing-derived specs can force it to
	// false: the chart default is true, which a plain bool couldn't
	// override to false through Merge.
	Enabled *bool `json:"enabled,omitempty"`
}

// ExternalEndpoint is the chart's externalEndpoint.* shape, restricted to
// the keys the catalog configures: whether the listener is enabled, the
// container port it binds to, the Service fronting it, and the Secret its
// certificate is terminated from.
type ExternalEndpoint struct {
	Enabled bool                    `json:"enabled,omitempty"`
	Port    int                     `json:"port,omitempty"`
	Service ExternalEndpointService `json:"service,omitzero"`
	TLS     ExternalEndpointTLS     `json:"tls,omitzero"`
}

// ExternalEndpointService is the chart's externalEndpoint.service.* shape,
// restricted to the keys the catalog configures.
type ExternalEndpointService struct {
	Type string `json:"type,omitempty"`
	Port int    `json:"port,omitempty"`
}

// ExternalEndpointTLS is the chart's externalEndpoint.tls.* shape,
// restricted to secretName: the catalog only exercises the
// existing-Secret form, never certManager.
type ExternalEndpointTLS struct {
	SecretName string `json:"secretName,omitempty"`
}

// Compatibility is the chart's compatibility.* shape (for testing only).
type Compatibility struct {
	// Version makes the manager behave like this older version, returning
	// Unimplemented for RPCs introduced after it.
	Version string `json:"version,omitempty"`
}

// TelepresenceAPI is the chart's telepresenceAPI.* shape, restricted to the
// port the catalog configures.
type TelepresenceAPI struct {
	Port int `json:"port,omitempty"`
}

// Workloads is the chart's workloads.* shape: which workload kinds the
// traffic-manager watches and the injector mutates
// (values.yaml: deployments/replicaSets/statefulSets default to true,
// argoRollouts defaults to false).
type Workloads struct {
	Deployments  WorkloadKind `json:"deployments,omitzero"`
	ReplicaSets  WorkloadKind `json:"replicaSets,omitzero"`
	StatefulSets WorkloadKind `json:"statefulSets,omitzero"`
	ArgoRollouts WorkloadKind `json:"argoRollouts,omitzero"`
}

// WorkloadKind is one workloads.<kind>.* entry.
type WorkloadKind struct {
	// Enabled is a pointer since a plain bool can't be distinguished from
	// "not set" by Merge, and the chart's own per-kind default varies
	// (true for Deployments/ReplicaSets/StatefulSets, false for
	// ArgoRollouts).
	Enabled *bool `json:"enabled,omitempty"`
}

// Intercept is the chart's intercept.* shape, restricted to the keys the
// catalog configures.
type Intercept struct {
	Environment InterceptEnvironment `json:"environment,omitzero"`
	// InactiveBlockTimeout is the chart's intercept.inactiveBlockTimeout
	// duration string (values.schema.yaml's "duration" $def), overriding
	// the chart's own 10m default: the maximum time an intercept may be
	// held by a client that is unreachable or inactive.
	InactiveBlockTimeout string `json:"inactiveBlockTimeout,omitempty"`
}

// InterceptEnvironment is the chart's intercept.environment.* shape.
type InterceptEnvironment struct {
	// Excluded lists environment variable names withheld from the client
	// at attachment time.
	Excluded []string `json:"excluded,omitempty"`
}

// Rbac is the chart's clientRbac shape.
type Rbac struct {
	Create   bool             `json:"create"`
	Subjects []rbacv1.Subject `json:"subjects,omitempty"`
}

// ManagerRbac is the chart's managerRbac shape, which unlike clientRbac has
// no subjects.
type ManagerRbac struct {
	Create bool `json:"create"`
}

// Timeouts is the chart's timeouts.* shape.
type Timeouts struct {
	AgentArrival string `json:"agentArrival,omitempty"`
}

// Usage is the chart's usage.* shape. Enabled has no omit option: usage
// reporting must be reachable through the overlay whether it's being turned
// on or (Baseline's case) kept off. CollectorAddress/Insecure matter only
// once a spec (UsageTo) turns Enabled on, so they're safe to omit when
// unset.
type Usage struct {
	Enabled          bool   `json:"enabled"`
	CollectorAddress string `json:"collectorAddress,omitempty"`
	Insecure         bool   `json:"insecure,omitempty"`
}

// Baseline returns the manager values every rtest spec starts from: debug
// logging, the given image coordinates for both the manager and the agent,
// usage reporting disabled, the agent-arrival timeout the framework's test
// waits are tuned for, a namespaceSelector matching ManagedNamespaceLabel=
// selectorLabel, and clientRbac/managerRbac granting TestServiceAccount
// access.
func Baseline(reg, tag, pullPolicy, selectorLabel string) Values {
	image := Image{Registry: reg, Tag: tag, PullPolicy: pullPolicy}
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      TestServiceAccount,
		Namespace: ManagerNamespace,
	}}
	return Values{
		LogLevel: "debug",
		Image:    image,
		Agent:    AgentValues{Image: image},
		ClientRbac: Rbac{
			Create:   true,
			Subjects: subjects,
		},
		ManagerRbac: ManagerRbac{Create: true},
		Timeouts:    Timeouts{AgentArrival: "60s"},
		// Requests only, no limits: their purpose is cgroup CPU weight, so
		// the (otherwise BestEffort) manager cannot be starved when many
		// test workload pods contend for a single node's CPU at once.
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
		// Expressed as a matchExpressions requirement rather than the
		// equivalent matchLabels form: released charts up to 2.31.x crash on
		// a matchLabels-only selector during any LATER manager install's
		// overlap validation (findings.md #1, fixed in this branch), and the
		// compat runs install those released charts.
		NamespaceSelector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{{
				Key:      ManagedNamespaceLabel,
				Operator: labels.OperatorIn,
				Values:   []string{selectorLabel},
			}},
		},
		Usage: Usage{Enabled: false},
	}
}

// Merge layers over on top of base: any field of over that is set (non-zero,
// non-nil, non-empty) replaces the corresponding field of base; unset fields
// keep base's value. It is used to apply a catalog spec's overlay on top of
// Baseline.
func Merge(base, over Values) Values {
	m := base
	if over.LogLevel != "" {
		m.LogLevel = over.LogLevel
	}
	m.Image = mergeImage(m.Image, over.Image)
	if over.Resources != nil {
		m.Resources = over.Resources
	}
	m.Agent.Image = mergeImage(m.Agent.Image, over.Agent.Image)
	if over.Agent.EnableH2cProbing != nil {
		m.Agent.EnableH2cProbing = over.Agent.EnableH2cProbing
	}
	m.AgentInjector = mergeAgentInjector(m.AgentInjector, over.AgentInjector)
	m.ClientRbac = mergeRbac(m.ClientRbac, over.ClientRbac)
	if over.ManagerRbac.Create {
		m.ManagerRbac.Create = true
	}
	if over.Timeouts.AgentArrival != "" {
		m.Timeouts.AgentArrival = over.Timeouts.AgentArrival
	}
	if over.NodeAgent.Enabled {
		m.NodeAgent.Enabled = true
	}
	m.Client = mergeClient(m.Client, over.Client)
	m.QuicTunnel = mergeQuicTunnel(m.QuicTunnel, over.QuicTunnel)
	m.Security = mergeSecurity(m.Security, over.Security)
	m.ExternalEndpoint = mergeExternalEndpoint(m.ExternalEndpoint, over.ExternalEndpoint)
	if over.Compatibility.Version != "" {
		m.Compatibility.Version = over.Compatibility.Version
	}
	if over.TelepresenceAPI.Port != 0 {
		m.TelepresenceAPI.Port = over.TelepresenceAPI.Port
	}
	m.Workloads = mergeWorkloads(m.Workloads, over.Workloads)
	m.Intercept = mergeIntercept(m.Intercept, over.Intercept)
	if over.NamespaceSelector != nil {
		m.NamespaceSelector = over.NamespaceSelector
	}
	if over.Namespaces != nil {
		// namespaces and namespaceSelector are mutually exclusive in the
		// chart: a static list always wins over (and nulls) the selector,
		// regardless of the order the two overlays are declared in.
		m.Namespaces = over.Namespaces
		m.NamespaceSelector = nil
	}
	if over.PodCIDRs != nil {
		m.PodCIDRs = over.PodCIDRs
	}
	if over.PodCIDRStrategy != "" {
		m.PodCIDRStrategy = over.PodCIDRStrategy
	}
	if over.ExtraEnv != nil {
		m.ExtraEnv = over.ExtraEnv
	}
	if over.ExtraVolumes != nil {
		m.ExtraVolumes = over.ExtraVolumes
	}
	if over.ExtraVolumeMounts != nil {
		m.ExtraVolumeMounts = over.ExtraVolumeMounts
	}
	if over.Usage.Enabled {
		m.Usage.Enabled = true
	}
	if over.Usage.CollectorAddress != "" {
		m.Usage.CollectorAddress = over.Usage.CollectorAddress
	}
	if over.Usage.Insecure {
		m.Usage.Insecure = true
	}
	return m
}

func mergeImage(base, over Image) Image {
	if over.Registry != "" {
		base.Registry = over.Registry
	}
	if over.Name != "" {
		base.Name = over.Name
	}
	if over.Tag != "" {
		base.Tag = over.Tag
	}
	if over.PullPolicy != "" {
		base.PullPolicy = over.PullPolicy
	}
	return base
}

func mergeRbac(base, over Rbac) Rbac {
	if over.Create {
		base.Create = true
	}
	if over.Subjects != nil {
		base.Subjects = over.Subjects
	}
	return base
}

func mergeAgentInjector(base, over AgentInjector) AgentInjector {
	if over.Enabled != nil {
		base.Enabled = over.Enabled
	}
	if over.InjectPolicy != "" {
		base.InjectPolicy = over.InjectPolicy
	}
	base.Certificate = mergeCertificate(base.Certificate, over.Certificate)
	base.Webhook = mergeWebhook(base.Webhook, over.Webhook)
	return base
}

func mergeCertificate(base, over Certificate) Certificate {
	if over.AccessMethod != "" {
		base.AccessMethod = over.AccessMethod
	}
	if over.Regenerate {
		base.Regenerate = true
	}
	return base
}

func mergeWebhook(base, over Webhook) Webhook {
	if over.ReinvocationPolicy != "" {
		base.ReinvocationPolicy = over.ReinvocationPolicy
	}
	return base
}

func mergeClient(base, over Client) Client {
	if over.NodeAgent.Enabled {
		base.NodeAgent.Enabled = true
	}
	if over.LogLevels.RootDaemon != "" {
		base.LogLevels.RootDaemon = over.LogLevels.RootDaemon
	}
	if over.LogLevels.UserDaemon != "" {
		base.LogLevels.UserDaemon = over.LogLevels.UserDaemon
	}
	if over.Routing.AlsoProxySubnets != nil {
		base.Routing.AlsoProxySubnets = over.Routing.AlsoProxySubnets
	}
	if over.Routing.NeverProxySubnets != nil {
		base.Routing.NeverProxySubnets = over.Routing.NeverProxySubnets
	}
	if over.Routing.AllowConflictingSubnets != nil {
		base.Routing.AllowConflictingSubnets = over.Routing.AllowConflictingSubnets
	}
	if over.Routing.AutoResolveConflicts != nil {
		base.Routing.AutoResolveConflicts = over.Routing.AutoResolveConflicts
	}
	if over.DNS.IncludeSuffixes != nil {
		base.DNS.IncludeSuffixes = over.DNS.IncludeSuffixes
	}
	return base
}

func mergeQuicTunnel(base, over QuicTunnel) QuicTunnel {
	if over.Enabled {
		base.Enabled = true
	}
	if over.Service.Type != "" {
		base.Service.Type = over.Service.Type
	}
	if over.Service.NodePort != 0 {
		base.Service.NodePort = over.Service.NodePort
	}
	if over.ExternalHost != "" {
		base.ExternalHost = over.ExternalHost
	}
	if over.ExternalPort != 0 {
		base.ExternalPort = over.ExternalPort
	}
	return base
}

func mergeWorkloads(base, over Workloads) Workloads {
	if over.Deployments.Enabled != nil {
		base.Deployments.Enabled = over.Deployments.Enabled
	}
	if over.ReplicaSets.Enabled != nil {
		base.ReplicaSets.Enabled = over.ReplicaSets.Enabled
	}
	if over.StatefulSets.Enabled != nil {
		base.StatefulSets.Enabled = over.StatefulSets.Enabled
	}
	if over.ArgoRollouts.Enabled != nil {
		base.ArgoRollouts.Enabled = over.ArgoRollouts.Enabled
	}
	return base
}

func mergeIntercept(base, over Intercept) Intercept {
	if over.Environment.Excluded != nil {
		base.Environment.Excluded = over.Environment.Excluded
	}
	if over.InactiveBlockTimeout != "" {
		base.InactiveBlockTimeout = over.InactiveBlockTimeout
	}
	return base
}

func mergeExternalEndpoint(base, over ExternalEndpoint) ExternalEndpoint {
	if over.Enabled {
		base.Enabled = true
	}
	if over.Port != 0 {
		base.Port = over.Port
	}
	if over.Service.Type != "" {
		base.Service.Type = over.Service.Type
	}
	if over.Service.Port != 0 {
		base.Service.Port = over.Service.Port
	}
	if over.TLS.SecretName != "" {
		base.TLS.SecretName = over.TLS.SecretName
	}
	return base
}

func mergeSecurity(base, over Security) Security {
	if over.Authentication.Mode != "" {
		base.Authentication.Mode = over.Authentication.Mode
	}
	if over.Authentication.X509.Enabled != nil {
		base.Authentication.X509.Enabled = over.Authentication.X509.Enabled
	}
	if over.Authorization.RequiredGrant != "" {
		base.Authorization.RequiredGrant = over.Authorization.RequiredGrant
	}
	return base
}
