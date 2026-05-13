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
	Registry                     string     `required:"true"`
	LogLevel                     slog.Level `required:"true"`
	User                         string
	ServerHost                   string
	ServerPort                   uint16 `required:"true"`
	PrometheusPort               uint16
	PrometheusDropClientLabel    bool
	MutatorWebhookPort           uint16
	ManagerNamespace             string
	AgentRestApiPort             uint16
	AgentArrivalTimeout          time.Duration
	MaxNamespaceSpecificWatchers int `default:"10"`

	GrpcMaxReceiveSize resource.Quantity

	PodCidrStrategy string
	PodCidrs        []netip.Prefix `default:"" envSeparator:" "`
	PodIp           netip.Addr

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
	AgentInitResources         *core.ResourceRequirements
	AgentInjectorName          string
	AgentInjectorSecret        string
	AgentInjectorMutationAware bool
	AgentSecurityContext       *core.SecurityContext
	AgentInitSecurityContext   *core.SecurityContext
	AgentInitContainerEnabled  bool `default:"true"`
	AgentMaxIdleTime           time.Duration
	AgentWatchRetryInterval    time.Duration `default:"10s"`

	ClientRoutingAlsoProxySubnets        []netip.Prefix `envSeparator:" "`
	ClientRoutingNeverProxySubnets       []netip.Prefix `envSeparator:" "`
	ClientRoutingAllowConflictingSubnets []netip.Prefix `envSeparator:" "`
	ClientDnsExcludeSuffixes             []string       `envSeparator:" "`
	ClientDnsIncludeSuffixes             []string       `envSeparator:" "`
	ClientConnectionTTL                  time.Duration

	EnabledWorkloadKinds k8sapi.Kinds `envSeparator:" " default:"Deployment StatefulSet ReplicaSet"`

	InterceptAllowGlobal          bool `default:"true"`
	InterceptInactiveBlockTimeout time.Duration

	// For testing only
	CompatibilityVersion *semver.Version
}

func (e *Env) GeneratorConfig(qualifiedAgentImage string) (*agentmap.GeneratorConfig, error) {
	return &agentmap.GeneratorConfig{
		AgentPort:           e.AgentPort,
		APIPort:             e.AgentRestApiPort,
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
