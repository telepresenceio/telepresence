package managerutil

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/envconfig"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
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
	LogLevel               string        `env:"LOG_LEVEL,                parser=logLevel"`
	User                   string        `env:"USER,                     parser=string,      default="`
	ServerHost             string        `env:"SERVER_HOST,              parser=string,      default="`
	ServerPort             uint16        `env:"SERVER_PORT,              parser=port-number"`
	PrometheusPort         uint16        `env:"PROMETHEUS_PORT,          parser=port-number, default=0"`
	MutatorWebhookPort     uint16        `env:"MUTATOR_WEBHOOK_PORT,     parser=port-number, default=0"`
	SystemAHost            string        `env:"SYSTEMA_HOST,             parser=string,      default="`
	SystemAPort            uint16        `env:"SYSTEMA_PORT,             parser=port-number, default=0"`
	ManagerNamespace       string        `env:"MANAGER_NAMESPACE,        parser=string,      default="`
	ManagedNamespaces      []string      `env:"MANAGED_NAMESPACES,       parser=split-trim,  default="`
	APIPort                uint16        `env:"AGENT_REST_API_PORT,      parser=port-number, default=0"`
	InterceptDisableGlobal bool          `env:"INTERCEPT_DISABLE_GLOBAL, parser=bool"`
	AgentArrivalTimeout    time.Duration `env:"AGENT_ARRIVAL_TIMEOUT,    parser=time.ParseDuration"`

	TracingGrpcPort uint16            `env:"TRACING_GRPC_PORT,     parser=port-number,default=0"`
	MaxReceiveSize  resource.Quantity `env:"GRPC_MAX_RECEIVE_SIZE, parser=quantity"`

	PodCIDRStrategy string       `env:"POD_CIDR_STRATEGY, parser=nonempty-string"`
	PodCIDRs        []*net.IPNet `env:"POD_CIDRS,         parser=split-ipnet, default="`
	PodIP           net.IP       `env:"POD_IP,            parser=ip"`

	AgentRegistry            string                      `env:"AGENT_REGISTRY,           parser=nonempty-string"`
	AgentImage               string                      `env:"AGENT_IMAGE,              parser=string,         default="`
	AgentImagePullSecrets    []core.LocalObjectReference `env:"AGENT_IMAGE_PULL_SECRETS, parser=json-local-refs,default="`
	AgentInjectPolicy        agentconfig.InjectPolicy    `env:"AGENT_INJECT_POLICY,      parser=enable-policy"`
	AgentAppProtocolStrategy k8sapi.AppProtocolStrategy  `env:"AGENT_APP_PROTO_STRATEGY, parser=app-proto-strategy"`
	AgentLogLevel            string                      `env:"AGENT_LOG_LEVEL,          parser=logLevel,       defaultFrom=LogLevel"`
	AgentPort                uint16                      `env:"AGENT_PORT,               parser=port-number"`
	AgentResources           *core.ResourceRequirements  `env:"AGENT_RESOURCES,          parser=json-resources, default="`
	AgentInitResources       *core.ResourceRequirements  `env:"AGENT_INIT_RESOURCES,     parser=json-resources, default="`
	AgentEnvoyLogLevel       string                      `env:"AGENT_ENVOY_LOG_LEVEL,    parser=logLevel,       defaultFrom=AgentLogLevel"`
	AgentEnvoyServerPort     uint16                      `env:"AGENT_ENVOY_SERVER_PORT,  parser=port-number"`
	AgentEnvoyAdminPort      uint16                      `env:"AGENT_ENVOY_ADMIN_PORT,   parser=port-number"`
	AgentInjectorName        string                      `env:"AGENT_INJECTOR_NAME,      parser=string"`

	ClientRoutingAlsoProxySubnets  []*net.IPNet  `env:"CLIENT_ROUTING_ALSO_PROXY_SUBNETS,  parser=split-ipnet, default="`
	ClientRoutingNeverProxySubnets []*net.IPNet  `env:"CLIENT_ROUTING_NEVER_PROXY_SUBNETS, parser=split-ipnet, default="`
	ClientDnsExcludeSuffixes       []string      `env:"CLIENT_DNS_EXCLUDE_SUFFIXES,        parser=split-trim"`
	ClientDnsIncludeSuffixes       []string      `env:"CLIENT_DNS_INCLUDE_SUFFIXES,        parser=split-trim,  default="`
	ClientConnectionTTL            time.Duration `env:"CLIENT_CONNECTION_TTL,              parser=time.ParseDuration"`
}

type envKey struct{}

func (e *Env) GeneratorConfig(qualifiedAgentImage string) (*agentmap.GeneratorConfig, error) {
	return &agentmap.GeneratorConfig{
		AgentPort:           e.AgentPort,
		APIPort:             e.APIPort,
		TracingPort:         e.TracingGrpcPort,
		ManagerPort:         e.ServerPort,
		QualifiedAgentImage: qualifiedAgentImage,
		ManagerNamespace:    e.ManagerNamespace,
		LogLevel:            e.AgentLogLevel,
		InitResources:       e.AgentInitResources,
		Resources:           e.AgentResources,
		PullSecrets:         e.AgentImagePullSecrets,
		EnvoyServerPort:     e.AgentEnvoyServerPort,
		EnvoyAdminPort:      e.AgentEnvoyAdminPort,
		EnvoyLogLevel:       e.AgentEnvoyLogLevel,
	}, nil
}

func (e *Env) QualifiedAgentImage() string {
	img := e.AgentImage
	if img == "" {
		return ""
	}
	return e.AgentRegistry + "/" + img
}

func fieldTypeHandlers() map[reflect.Type]envconfig.FieldTypeHandler {
	fhs := envconfig.DefaultFieldTypeHandlers()
	fp := fhs[reflect.TypeOf("")]
	fp.Parsers["string"] = fp.Parsers["possibly-empty-string"]
	fp.Parsers["logLevel"] = fp.Parsers["logrus.ParseLevel"]
	fp = fhs[reflect.TypeOf(true)]
	fp.Parsers["bool"] = fp.Parsers["strconv.ParseBool"]
	fhs[reflect.TypeOf(uint16(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"port-number": func(str string) (any, error) {
				pn, err := strconv.ParseUint(str, 10, 16)
				return uint16(pn), err
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.SetUint(uint64(src.(uint16))) },
	}
	fhs[reflect.TypeOf(k8sapi.AppProtocolStrategy(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"app-proto-strategy": func(str string) (any, error) {
				return k8sapi.NewAppProtocolStrategy(str)
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.SetInt(int64(src.(k8sapi.AppProtocolStrategy))) },
	}
	fhs[reflect.TypeOf(agentconfig.InjectPolicy(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"enable-policy": func(str string) (any, error) {
				return agentconfig.NewEnablePolicy(str)
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.SetInt(int64(src.(agentconfig.InjectPolicy))) },
	}
	fhs[reflect.TypeOf(resource.Quantity{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"quantity": func(str string) (any, error) {
				return resource.ParseQuantity(str)
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.(resource.Quantity))) },
	}
	fhs[reflect.TypeOf(net.IP{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"ip": func(str string) (any, error) { //nolint:unparam // API requirement
				return iputil.Parse(str), nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.(net.IP))) },
	}
	fhs[reflect.TypeOf([]string{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"split-trim": func(str string) (any, error) { //nolint:unparam // API requirement
				if len(str) == 0 {
					return nil, nil
				}
				ss := strings.Split(str, " ")
				for i, s := range ss {
					ss[i] = strings.TrimSpace(s)
				}
				return ss, nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.([]string))) },
	}
	fhs[reflect.TypeOf([]*net.IPNet{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"split-ipnet": func(str string) (any, error) {
				if len(str) == 0 {
					return nil, nil
				}
				ss := strings.Split(str, " ")
				ns := make([]*net.IPNet, len(ss))
				for i, s := range ss {
					var err error
					if _, ns[i], err = net.ParseCIDR(strings.TrimSpace(s)); err != nil {
						return nil, err
					}
				}
				return ns, nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.([]*net.IPNet))) },
	}
	fhs[reflect.TypeOf([]core.LocalObjectReference{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"json-local-refs": func(js string) (any, error) {
				if js == "" {
					return nil, nil
				}
				var rr []core.LocalObjectReference
				if err := json.Unmarshal([]byte(js), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.([]core.LocalObjectReference))) },
	}
	fhs[reflect.TypeOf(&core.ResourceRequirements{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"json-resources": func(js string) (any, error) {
				if js == "" {
					return nil, nil
				}
				var rr *core.ResourceRequirements
				if err := json.Unmarshal([]byte(js), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.(*core.ResourceRequirements))) },
	}
	return fhs
}

func LoadEnv(ctx context.Context, lookupFunc func(string) (string, bool)) (context.Context, error) {
	env := Env{}
	parser, err := envconfig.GenerateParser(reflect.TypeOf(env), fieldTypeHandlers())
	if err != nil {
		panic(err)
	}
	var errs derror.MultiError
	warn, fatal := parser.ParseFromEnv(&env, lookupFunc)
	errs = append(errs, warn...)
	errs = append(errs, fatal...)
	if len(errs) > 0 {
		return ctx, errs
	}
	return WithEnv(ctx, &env), nil
}

func WithEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func GetEnv(ctx context.Context) *Env {
	env, ok := ctx.Value(envKey{}).(*Env)
	if !ok {
		return nil
	}
	return env
}
