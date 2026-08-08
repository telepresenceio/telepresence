package managerutil

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/caarlos0/env/v11"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Env is the traffic-manager's environment. It does not define any defaults because all
// defaults are declared in the Helm chart that creates the deployment. The reason for this
// is that some defaults are needed in other places in the Helm chart. In other words, since
// the Helm chart needs access to all defaults, and the traffic-manager only needs a subset,
// it's better to declare defaults in the Helm chart.
//
// The Env is responsible for all parsing of the environment strings. No parsing of such
// strings should be made elsewhere in the code.
type Env struct {
	Registry                  string     `required:"true"`
	LogLevel                  slog.Level `required:"true"`
	User                      string
	ServerHost                string
	ServerPort                uint16 `required:"true"`
	PrometheusPort            uint16
	PrometheusDropClientLabel bool
	// MutatorWebhookPort is the port the webhook server (or, for a
	// node-agent-only install with the injector disabled, the plain-HTTP
	// /uninstall-only server) listens on. The chart only sets
	// MUTATOR_WEBHOOK_PORT when agentInjector.enabled, so the default here
	// must match agentInjector.webhook.port's chart default (values.yaml)
	// -- the agent-injector Service's targetPort is unconditional on that
	// value regardless of which mode created the listening process.
	MutatorWebhookPort           uint16 `default:"8443"`
	ManagerNamespace             string
	AgentRestApiPort             uint16
	AgentArrivalTimeout          time.Duration `default:"30s"`
	MaxNamespaceSpecificWatchers int           `default:"10"`

	GrpcMaxReceiveSize resource.Quantity

	PodCidrStrategy string
	PodCidrs        []netip.Prefix `default:"" envSeparator:" "`
	PodIp           netip.Addr
	PodHostIp       netip.Addr

	AgentRegistry              string
	AgentImageName             string
	AgentImageTag              string
	AgentImagePullPolicy       string
	AgentImagePullSecrets      []core.LocalObjectReference `envSeparator:" "`
	AgentInjectPolicy          agentconfig.InjectPolicy    `default:"Never"`
	AgentLogLevel              slog.Level                  `env:",expand" default:"${LOG_LEVEL}"`
	AgentPort                  uint16
	AgentEnableH2cProbing      bool
	AgentConsumptionMetrics    bool `default:"true"`
	AgentResources             *core.ResourceRequirements
	AgentMountPolicies         types.MountPolicies
	AgentMeshDialSubnets       []netip.Prefix `envSeparator:" "`
	AgentInitResources         *core.ResourceRequirements
	AgentInjectorName          string
	AgentInjectorSecret        string
	AgentInjectorMutationAware bool
	AgentSecurityContext       *core.SecurityContext
	AgentInitSecurityContext   *core.SecurityContext
	AgentInitContainerEnabled  bool `default:"true"`
	AgentMaxIdleTime           time.Duration
	AgentWatchRetryInterval    time.Duration `default:"10s"`

	// GoCoverDir is the manager's own GOCOVERDIR, propagated into generated agent containers.
	GoCoverDir string `env:"GOCOVERDIR"`

	// NodeAgentEnabled controls whether this traffic-manager will provision
	// node-hosted traffic-agents (manager-created Jobs that enter the target
	// pod's namespaces) when requested by a client. Defaults to false.
	NodeAgentEnabled bool

	// NodeAgentCRISocket is the path of the container-runtime socket that is
	// mounted into node-agent Jobs so that they can resolve a target
	// container's process ID (Helm value nodeAgent.criSocket). When empty,
	// the Jobs mount the node's /run directory instead and the agent probes
	// the well-known CRI sockets beneath it.
	NodeAgentCRISocket string

	// TunnelQuicPort is the UDP port the traffic-manager's QUIC tunnel listener
	// binds to on all interfaces. Zero (the default) disables the listener.
	TunnelQuicPort uint16

	// AuthX509Port is the TCP port the traffic-manager's x509 auth-only TLS
	// listener binds to on all interfaces. Zero (the default) disables the
	// listener.
	AuthX509Port uint16

	// TunnelQuicExternalHost is the externally reachable host or IP advertised to
	// clients for the QUIC tunnel endpoint, overriding candidate discovery
	// entirely (see "Zero-configuration endpoint discovery" in
	// docs/reference/quic-transport-architecture.md). When unset, GetQuicTunnelEndpoint
	// advertises whatever quicDiscovery has found instead; the endpoint is
	// enabled either way as soon as the listener is enabled and at least one
	// candidate (explicit or discovered) exists.
	TunnelQuicExternalHost string

	// TunnelQuicServiceName is the name of the Service the QUIC forwarder listens
	// behind (charts/telepresence-oss/templates/quicforwarder.yaml's
	// "traffic-manager-quic" Service), in the manager's own namespace. Discovery
	// watches this Service -- and, for a NodePort Service, the cluster's Nodes --
	// to derive the candidate list. Empty disables discovery (an older chart, or
	// quicTunnel.service.create == false); TunnelQuicExternalHost still works in
	// that case, discovery just never starts.
	TunnelQuicServiceName string

	// TunnelQuicExternalPort is the UDP port advertised to clients for the QUIC
	// tunnel endpoint. Zero (the default) means the same port as TunnelQuicPort,
	// i.e. the Service in front of the listener doesn't remap it.
	TunnelQuicExternalPort uint16

	// TunnelQuicAgentPort is the UDP port a traffic-agent's own QUIC listener
	// binds to, delivered to agent pods (sidecar and node-agent alike) as the
	// AGENT_QUIC_PORT environment variable via the generated agent config, only
	// when the QUIC tunnel is enabled on this manager (TunnelQuicPort != 0). It
	// is deliberately not the manager's own TunnelQuicPort: a sidecar agent
	// shares its pod's network namespace with the application, so its QUIC
	// listener needs a port of its own. This has a Go-level default (unlike most
	// of Env, see the comment on MutatorWebhookPort above) so that a manager and
	// its agents agree on a port even when the chart's quicTunnel.agentPort
	// value doesn't reach the manager (a chart predating the value, or one that
	// sets it to zero).
	TunnelQuicAgentPort uint16 `default:"7787"`

	ClientRoutingAlsoProxySubnets        []netip.Prefix `envSeparator:" "`
	ClientRoutingNeverProxySubnets       []netip.Prefix `envSeparator:" "`
	ClientRoutingAllowConflictingSubnets []netip.Prefix `envSeparator:" "`
	ClientDnsExcludeSuffixes             []string       `envSeparator:" "`
	ClientDnsIncludeSuffixes             []string       `envSeparator:" "`
	ClientConnectionTTL                  time.Duration

	EnabledWorkloadKinds k8sapi.Kinds `envSeparator:" " default:"Deployment StatefulSet ReplicaSet"`

	InterceptAllowGlobal          bool `default:"true"`
	InterceptInactiveBlockTimeout time.Duration

	// AuthenticationMode controls how strictly the traffic-manager enforces
	// caller authentication (disabled, permissive, or enforcing).
	AuthenticationMode auth.Mode `default:"permissive"`

	// AuthorizationGate controls which grant the manager accepts as
	// authorization to connect and to attach to a workload: pods/portforward
	// alone, the telepresence.io group's own attributes alone, or either
	// (portforward, telepresence, or any).
	AuthorizationGate auth.Gate `default:"any"`

	// Anonymous usage reporting. The manager produces reports whose only
	// identifier is the UUID stored in the traffic-manager-install-id
	// ConfigMap. UsageReportingEnabled is opt-out (default true). An empty
	// UsageCollectorAddress means produced reports never leave the process —
	// they age out of the FIFO.
	UsageReportingEnabled  bool `default:"true"`
	UsageCollectorAddress  string
	UsageCollectorInsecure bool

	// For testing only
	CompatibilityVersion *semver.Version
}

// HostNetwork reports whether the traffic-manager pod runs on the host
// network, in which case its pod IP is a node address rather than an address
// in the cluster's pod CIDR. Detection requires the POD_HOST_IP environment
// variable; when that is absent the manager is assumed to be on the pod
// network.
func (e *Env) HostNetwork() bool {
	return e.PodHostIp.IsValid() && e.PodHostIp == e.PodIp
}

func (e *Env) GeneratorConfig(qualifiedAgentImage string) (*agentmap.GeneratorConfig, error) {
	// QuicPort is only delivered to agents when this manager's own QUIC tunnel
	// listener is enabled; an agent QUIC listener is useless if there is no
	// manager-side CA to mint it a certificate (GetQuicAgentCert) or forwarder
	// backend entry (WatchQuicBackends) to route through.
	var quicPort uint16
	if e.TunnelQuicPort != 0 {
		quicPort = e.TunnelQuicAgentPort
	}
	return &agentmap.GeneratorConfig{
		AgentPort:           e.AgentPort,
		APIPort:             e.AgentRestApiPort,
		QuicPort:            quicPort,
		ClientConnectionTTL: e.ClientConnectionTTL,
		ManagerPort:         e.ServerPort,
		QualifiedAgentImage: qualifiedAgentImage,
		ManagerNamespace:    e.ManagerNamespace,
		ClusterDomain:       e.clusterDomain(),
		LogLevel:            e.AgentLogLevel,
		InitResources:       e.AgentInitResources,
		Resources:           e.AgentResources,
		PullPolicy:          e.AgentImagePullPolicy,
		PullSecrets:         e.AgentImagePullSecrets,
		SecurityContext:     e.AgentSecurityContext,
		InitSecurityContext: e.AgentInitSecurityContext,
		MountPolicies:       e.AgentMountPolicies,
		MeshDialSubnets:     e.AgentMeshDialSubnets,
		EnableH2cProbing:    e.AgentEnableH2cProbing,
		EnableMetrics:       e.AgentConsumptionMetrics && e.PrometheusPort != 0,
		WatchRetryInterval:  e.AgentWatchRetryInterval,
	}, nil
}

func (e *Env) clusterDomain() string {
	conf, err := dnsproxy.ReadResolveFile("/etc/resolv.conf")
	if err != nil || len(conf.Search) < 3 {
		return ""
	}
	nsSvc := e.ManagerNamespace + ".svc."
	if !strings.HasPrefix(conf.Search[0], nsSvc) || !strings.HasPrefix(conf.Search[1], "svc.") {
		return ""
	}
	domain := strings.TrimPrefix(conf.Search[1], "svc.")
	third := conf.Search[2]
	if !strings.HasSuffix(domain, ".") {
		domain += "."
	}
	if !strings.HasSuffix(third, ".") {
		third += "."
	}
	if !strings.EqualFold(third, domain) {
		return ""
	}
	return strings.TrimSuffix(domain, ".")
}

func (e *Env) QualifiedAgentImage() string {
	img := e.AgentImageName
	if img == "" {
		return ""
	}
	img = e.AgentRegistry + "/" + img
	if e.AgentImageTag != "" {
		img += ":" + e.AgentImageTag
	}
	return img
}

type envKey struct{}

func LoadEnv(ctx context.Context, envMap map[string]string) (context.Context, error) {
	envStruct := new(Env)
	err := env.ParseWithOptions(envStruct, env.Options{
		Environment:           envMap,
		DefaultValueTagName:   "default",
		UseFieldNameByDefault: true,
		FuncMap: map[reflect.Type]env.ParserFunc{
			reflect.TypeOf(slog.Level(0)): func(s string) (any, error) {
				if strings.EqualFold(s, "warning") {
					s = "warn"
				}
				return clog.ParseLevel(s)
			},
			reflect.TypeOf([]core.LocalObjectReference{}): func(s string) (any, error) {
				var rr []core.LocalObjectReference
				if err := json.Unmarshal([]byte(s), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
			reflect.TypeOf(types.MountPolicies{}): func(s string) (any, error) {
				var rr types.MountPolicies
				if err := json.Unmarshal([]byte(s), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
			reflect.TypeOf(resource.Quantity{}): func(s string) (any, error) {
				return resource.ParseQuantity(s)
			},
			reflect.TypeOf(core.ResourceRequirements{}): func(s string) (any, error) {
				var rr core.ResourceRequirements
				if err := json.Unmarshal([]byte(s), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
			reflect.TypeOf(core.SecurityContext{}): func(s string) (any, error) {
				var rr core.SecurityContext
				if err := json.Unmarshal([]byte(s), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
			reflect.TypeOf(semver.Version{}): func(s string) (any, error) {
				return semver.Parse(s)
			},
		},
	})
	if err != nil {
		return ctx, err
	}
	return WithEnv(ctx, envStruct), nil
}

func WithEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func GetEnv(ctx context.Context) *Env {
	if env, ok := ctx.Value(envKey{}).(*Env); ok {
		return env
	}
	panic("no Env has been set")
}
