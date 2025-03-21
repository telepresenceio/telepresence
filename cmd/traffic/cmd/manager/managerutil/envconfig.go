package managerutil

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/envconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
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
	Registry                     string        `env:"REGISTRY,                 parser=nonempty-string"`
	LogLevel                     string        `env:"LOG_LEVEL,                parser=logLevel"`
	User                         string        `env:"USER,                     parser=string,      default="`
	ServerHost                   string        `env:"SERVER_HOST,              parser=string,      default="`
	ServerPort                   uint16        `env:"SERVER_PORT,              parser=port-number"`
	PrometheusPort               uint16        `env:"PROMETHEUS_PORT,          parser=port-number, default=0"`
	MutatorWebhookPort           uint16        `env:"MUTATOR_WEBHOOK_PORT,     parser=port-number, default=0"`
	ManagerNamespace             string        `env:"MANAGER_NAMESPACE,        parser=string,      default="`
	APIPort                      uint16        `env:"AGENT_REST_API_PORT,      parser=port-number, default=0"`
	AgentArrivalTimeout          time.Duration `env:"AGENT_ARRIVAL_TIMEOUT,    parser=time.ParseDuration, default=0"`
	MaxNamespaceSpecificWatchers int           `env:"MAX_NAMESPACE_SPECIFIC_WATCHERS,parser=int,  default=10"`

	MaxReceiveSize resource.Quantity `env:"GRPC_MAX_RECEIVE_SIZE, parser=quantity"`

	PodCIDRStrategy string         `env:"POD_CIDR_STRATEGY, parser=nonempty-string"`
	PodCIDRs        []netip.Prefix `env:"POD_CIDRS,         parser=split-ipnet, default="`
	PodIP           netip.Addr     `env:"POD_IP,            parser=ip"`

	AgentRegistry            string                      `env:"AGENT_REGISTRY,                parser=string,         default="`
	AgentImageName           string                      `env:"AGENT_IMAGE_NAME,              parser=string,         default="`
	AgentImageTag            string                      `env:"AGENT_IMAGE_TAG,               parser=string,         default="`
	AgentImagePullPolicy     string                      `env:"AGENT_IMAGE_PULL_POLICY,       parser=string,         default="`
	AgentImagePullSecrets    []core.LocalObjectReference `env:"AGENT_IMAGE_PULL_SECRETS,      parser=json-local-refs,default="`
	AgentInjectPolicy        agentconfig.InjectPolicy    `env:"AGENT_INJECT_POLICY,           parser=enable-policy,  default=Never"`
	AgentAppProtocolStrategy k8sapi.AppProtocolStrategy  `env:"AGENT_APP_PROTO_STRATEGY,      parser=app-proto-strategy, default=http2Probe"`
	AgentLogLevel            string                      `env:"AGENT_LOG_LEVEL,               parser=logLevel,       defaultFrom=LogLevel"`
	AgentPort                uint16                      `env:"AGENT_PORT,                    parser=port-number,    default=0"`
	AgentResources           *core.ResourceRequirements  `env:"AGENT_RESOURCES,               parser=json-resources, default="`
	AgentMountPolicies       types.MountPolicies         `env:"AGENT_MOUNT_POLICIES,          parser=json-mount-policies, default="`
	AgentInitResources       *core.ResourceRequirements  `env:"AGENT_INIT_RESOURCES,          parser=json-resources, default="`
	AgentInjectorName        string                      `env:"AGENT_INJECTOR_NAME,           parser=string,         default="`
	AgentInjectorSecret      string                      `env:"AGENT_INJECTOR_SECRET,         parser=string,         default="`
	AgentSecurityContext     *core.SecurityContext       `env:"AGENT_SECURITY_CONTEXT,        parser=json-security-context, default="`
	AgentInitSecurityContext *core.SecurityContext       `env:"AGENT_INIT_SECURITY_CONTEXT,   parser=json-security-context, default="`

	ClientRoutingAlsoProxySubnets        []netip.Prefix `env:"CLIENT_ROUTING_ALSO_PROXY_SUBNETS,  		parser=split-ipnet, default="`
	ClientRoutingNeverProxySubnets       []netip.Prefix `env:"CLIENT_ROUTING_NEVER_PROXY_SUBNETS, 		parser=split-ipnet, default="`
	ClientRoutingAllowConflictingSubnets []netip.Prefix `env:"CLIENT_ROUTING_ALLOW_CONFLICTING_SUBNETS, 	parser=split-ipnet, default="`
	ClientDnsExcludeSuffixes             []string       `env:"CLIENT_DNS_EXCLUDE_SUFFIXES,        		parser=split-trim"`
	ClientDnsIncludeSuffixes             []string       `env:"CLIENT_DNS_INCLUDE_SUFFIXES,       		parser=split-trim,  default="`
	ClientConnectionTTL                  time.Duration  `env:"CLIENT_CONNECTION_TTL,              		parser=time.ParseDuration"`

	EnabledWorkloadKinds k8sapi.Kinds `env:"ENABLED_WORKLOAD_KINDS, parser=split-trim, default=Deployment StatefulSet ReplicaSet"`

	// For testing only
	CompatibilityVersion *semver.Version `env:"COMPATIBILITY_VERSION, parser=version, default="`
}

func (e *Env) GeneratorConfig(qualifiedAgentImage string) (agentmap.GeneratorConfig, error) {
	return &agentmap.BasicGeneratorConfig{
		AgentPort:           e.AgentPort,
		APIPort:             e.APIPort,
		ManagerPort:         e.ServerPort,
		QualifiedAgentImage: qualifiedAgentImage,
		ManagerNamespace:    e.ManagerNamespace,
		LogLevel:            e.AgentLogLevel,
		InitResources:       e.AgentInitResources,
		Resources:           e.AgentResources,
		PullPolicy:          e.AgentImagePullPolicy,
		PullSecrets:         e.AgentImagePullSecrets,
		AppProtocolStrategy: e.AgentAppProtocolStrategy,
		SecurityContext:     e.AgentSecurityContext,
		InitSecurityContext: e.AgentInitSecurityContext,
		MountPolicies:       e.AgentMountPolicies,
	}, nil
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

func fieldTypeHandlers() map[reflect.Type]envconfig.FieldTypeHandler {
	fhs := envconfig.DefaultFieldTypeHandlers()
	fp := fhs[reflect.TypeOf("")]
	fp.Parsers["string"] = fp.Parsers["possibly-empty-string"]
	fp.Parsers["logLevel"] = fp.Parsers["logrus.ParseLevel"]
	fp = fhs[reflect.TypeOf(0)]
	fp.Parsers["int"] = fp.Parsers["strconv.ParseInt"]
	fp = fhs[reflect.TypeOf(true)]
	fp.Parsers["bool"] = fp.Parsers["strconv.ParseBool"]
	fhs[reflect.TypeOf(uint16(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"port-number": func(str string) (any, error) {
				pn, err := strconv.ParseUint(str, 10, 16)
				return uint16(pn), err
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.SetUint(uint64(src.(uint16))) },
	}
	fhs[reflect.TypeOf(k8sapi.AppProtocolStrategy(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"app-proto-strategy": func(str string) (any, error) {
				return k8sapi.NewAppProtocolStrategy(str)
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.SetInt(int64(src.(k8sapi.AppProtocolStrategy))) },
	}
	fhs[reflect.TypeOf(agentconfig.InjectPolicy(0))] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"enable-policy": func(str string) (any, error) {
				return agentconfig.NewEnablePolicy(str)
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.SetInt(int64(src.(agentconfig.InjectPolicy))) },
	}
	fhs[reflect.TypeOf(resource.Quantity{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"quantity": func(str string) (any, error) {
				return resource.ParseQuantity(str)
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(resource.Quantity))) },
	}
	fhs[reflect.TypeOf(netip.Addr{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"ip": func(str string) (any, error) { //nolint:unparam // API requirement
				return netip.ParseAddr(str)
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(netip.Addr))) },
	}
	fhs[reflect.TypeOf(types.MountPolicies{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"json-mount-policies": func(js string) (any, error) {
				if js == "" {
					return nil, nil
				}
				var mps types.MountPolicies
				if err := json.Unmarshal([]byte(js), &mps); err != nil {
					return nil, err
				}
				return mps, nil
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(types.MountPolicies))) },
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
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.([]string))) },
	}
	fhs[reflect.TypeOf([]netip.Prefix{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"split-ipnet": func(str string) (any, error) {
				if len(str) == 0 {
					return nil, nil
				}
				ss := strings.Split(str, " ")
				ns := make([]netip.Prefix, len(ss))
				for i, s := range ss {
					var err error
					if ns[i], err = netip.ParsePrefix(strings.TrimSpace(s)); err != nil {
						return nil, err
					}
				}
				return ns, nil
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.([]netip.Prefix))) },
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
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.([]core.LocalObjectReference))) },
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
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(*core.ResourceRequirements))) },
	}
	fhs[reflect.TypeOf(&core.SecurityContext{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"json-security-context": func(js string) (any, error) {
				if js == "" {
					return nil, nil
				}
				var rr *core.SecurityContext
				if err := json.Unmarshal([]byte(js), &rr); err != nil {
					return nil, err
				}
				return rr, nil
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(*core.SecurityContext))) },
	}
	fhs[reflect.TypeOf(true)] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"bool": func(str string) (any, error) {
				return strconv.ParseBool(str)
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.SetBool(src.(bool)) },
	}
	fhs[reflect.TypeOf(&semver.Version{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"version": func(str string) (any, error) {
				if str == "" {
					return nil, nil
				}
				v, err := semver.Parse(str)
				if err != nil {
					return nil, err
				}
				return &v, nil
			},
		},
		Setter: func(dst reflect.Value, src any) { dst.Set(reflect.ValueOf(src.(*semver.Version))) },
	}
	fhs[reflect.TypeOf(k8sapi.Kinds{})] = envconfig.FieldTypeHandler{
		Parsers: map[string]func(string) (any, error){
			"split-trim": func(str string) (any, error) { //nolint:unparam // API requirement
				if len(str) == 0 {
					return nil, nil
				}
				ss := strings.Split(str, " ")
				ks := make(k8sapi.Kinds, len(ss))
				for i, s := range ss {
					ks[i] = k8sapi.Kind(s)
					if !k8sapi.KnownWorkloadKinds.Contains(ks[i]) {
						return nil, fmt.Errorf("invalid workload kind: %q", s)
					}
				}
				return ks, nil
			},
		},
		Setter: func(dst reflect.Value, src interface{}) { dst.Set(reflect.ValueOf(src.(k8sapi.Kinds))) },
	}
	return fhs
}

type envKey struct{}

func LoadEnv(ctx context.Context, lookupFunc func(string) (string, bool)) (context.Context, error) {
	env, err := LoadEnvInto(Env{}, lookupFunc)
	if err != nil {
		return ctx, err
	}
	return WithEnv(ctx, env.(*Env)), nil
}

func LoadEnvInto(env any, lookupFunc func(string) (string, bool)) (any, error) {
	et := reflect.ValueOf(env)
	parser, err := envconfig.GenerateParser(et.Type(), fieldTypeHandlers())
	if err != nil {
		panic(err)
	}
	var errs derror.MultiError
	ptr := reflect.New(et.Type())
	ptr.Elem().Set(et)
	warn, fatal := parser.ParseFromEnv(ptr.Interface(), lookupFunc)
	errs = append(errs, warn...)
	errs = append(errs, fatal...)
	if len(errs) > 0 {
		return nil, errs
	}
	return ptr.Interface(), nil
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
