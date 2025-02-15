package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/durationpb"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type DefaultsAware interface {
	defaults() DefaultsAware
	IsZero() bool
	MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error
	UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error
}

func jsonName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	if jt := f.Tag.Get("json"); len(jt) > 0 {
		if jt == "-" {
			return ""
		}
		if ci := strings.IndexByte(jt, ','); ci > 0 {
			return jt[:ci]
		}
		return jt
	}
	return f.Name
}

// mapWithoutDefaults returns a map with all values in the given struct that are not equal to their corresponding default value.
func mapWithoutDefaults[T DefaultsAware](sourceStruct T) map[string]any {
	m := make(map[string]any)
	sv := reflect.ValueOf(sourceStruct).Elem()
	dv := reflect.ValueOf(sourceStruct.defaults()).Elem()
	vt := sv.Type()
	for _, f := range reflect.VisibleFields(vt) {
		if n := jsonName(f); n != "" {
			sf := sv.FieldByIndex(f.Index).Interface()
			if !reflect.DeepEqual(sf, dv.FieldByIndex(f.Index).Interface()) {
				m[n] = sf
			}
		}
	}
	return m
}

// mapWithoutDefaults will merge non-default values from sourceStruct into targetStruct.
func mergeNonDefaults[T DefaultsAware](targetStruct T, sourceStruct T) {
	tv := reflect.ValueOf(targetStruct).Elem()
	sv := reflect.ValueOf(sourceStruct).Elem()
	dv := reflect.ValueOf(targetStruct.defaults()).Elem()
	vt := tv.Type()
	for _, f := range reflect.VisibleFields(vt) {
		if jsonName(f) != "" {
			sf := sv.FieldByIndex(f.Index)
			if !reflect.DeepEqual(sf.Interface(), dv.FieldByIndex(f.Index).Interface()) {
				tv.FieldByIndex(f.Index).Set(sf)
			}
		}
	}
}

// isDefault returns true if the given struct is equal to its default.
func isDefault[T DefaultsAware](sourceStruct T) bool {
	return reflect.DeepEqual(sourceStruct, sourceStruct.defaults())
}

const ConfigFile = "config.yml"

type Config interface {
	fmt.Stringer
	MarshalYAML() ([]byte, error)
	OSSpecific() *OSSpecificConfig
	Base() *BaseConfig
	Timeouts() *Timeouts
	LogLevels() *LogLevels
	Images() *Images
	Grpc() *Grpc
	TelepresenceAPI() *TelepresenceAPI
	Intercept() *Intercept
	Cluster() *Cluster
	DNS() *DNS
	Routing() *Routing
	DestructiveMerge(Config)
	Merge(priority Config) Config
}

// BaseConfig contains all configuration values for the telepresence CLI.
type BaseConfig struct {
	OSSpecificConfig ``
	TimeoutsV        Timeouts        `json:"timeouts,omitzero"`
	LogLevelsV       LogLevels       `json:"logLevels,omitzero"`
	ImagesV          Images          `json:"images,omitzero"`
	GrpcV            Grpc            `json:"grpc,omitzero"`
	TelepresenceAPIV TelepresenceAPI `json:"telepresenceAPI,omitzero"`
	InterceptV       Intercept       `json:"intercept,omitzero"`
	ClusterV         Cluster         `json:"cluster,omitzero"`
	DNSV             DNS             `json:"dns,omitzero"`
	RoutingV         Routing         `json:"routing,omitzero"`

	// This is actually a traffic-manager setting, and controls
	// the agent's connection to the client.
	DummyConnectionTTL time.Duration `json:"connectionTTL,omitzero"`
}

func (c *BaseConfig) OSSpecific() *OSSpecificConfig {
	return &c.OSSpecificConfig
}

func (c *BaseConfig) Base() *BaseConfig {
	return c
}

func (c *BaseConfig) Timeouts() *Timeouts {
	return &c.TimeoutsV
}

func (c *BaseConfig) LogLevels() *LogLevels {
	return &c.LogLevelsV
}

func (c *BaseConfig) Images() *Images {
	return &c.ImagesV
}

func (c *BaseConfig) Grpc() *Grpc {
	return &c.GrpcV
}

func (c *BaseConfig) TelepresenceAPI() *TelepresenceAPI {
	return &c.TelepresenceAPIV
}

func (c *BaseConfig) Intercept() *Intercept {
	return &c.InterceptV
}

func (c *BaseConfig) Cluster() *Cluster {
	return &c.ClusterV
}

func (c *BaseConfig) DNS() *DNS {
	return &c.DNSV
}

func (c *BaseConfig) Routing() *Routing {
	return &c.RoutingV
}

func (c *BaseConfig) MarshalYAML() ([]byte, error) {
	data, err := MarshalJSON(c)
	if err == nil {
		data, err = yaml.JSONToYAML(data)
	}
	return data, err
}

func UnmarshalJSON(data []byte, into any, rejectUnknown bool) error {
	opts := []json.Options{
		json.WithUnmarshalers(
			json.UnmarshalFromFunc(func(dec *jsontext.Decoder, strategy *k8sapi.AppProtocolStrategy, opts json.Options) error {
				var s string
				if err := json.UnmarshalDecode(dec, &s, opts); err != nil {
					return err
				}
				return strategy.EnvDecode(s)
			})),
	}
	if rejectUnknown {
		opts = append(opts, json.RejectUnknownMembers(true))
	}
	if err := json.Unmarshal(data, into, opts...); err != nil {
		return err
	}
	return nil
}

func MarshalJSON(value any) ([]byte, error) {
	return json.Marshal(value, json.WithMarshalers(
		json.MarshalToFunc[k8sapi.AppProtocolStrategy](func(enc *jsontext.Encoder, strategy k8sapi.AppProtocolStrategy, _ json.Options) error {
			return enc.WriteToken(jsontext.String(strategy.String()))
		}),
	))
}

func UnmarshalJSONConfig(data []byte, rejectUnknown bool) (Config, error) {
	cfg := GetDefaultConfig()
	if err := UnmarshalJSON(data, cfg, rejectUnknown); err != nil {
		return nil, err
	}
	return cfg, nil
}

func ParseConfigYAML(ctx context.Context, path string, data []byte) (Config, error) {
	data, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	cfg, err := UnmarshalJSONConfig(data, true)
	if err != nil {
		var semanticErr *json.SemanticError
		if errors.As(err, &semanticErr) && strings.Contains(semanticErr.Error(), "unknown object member name ") {
			s := semanticErr.Error()
			// Strip unnecessarily verbose text from the message, but retain the type.
			if m := regexp.MustCompile(`json:.+ of type (.*)$`).FindStringSubmatch(s); len(m) == 2 {
				s = m[1]
			}
			dlog.Errorf(ctx, "%s: %v", path, s)
			cfg, err = UnmarshalJSONConfig(data, false)
		}
		if err != nil {
			return nil, err
		}
	}
	if cfg.Routing().VirtualSubnet == defaultVirtualSubnet && cfg.Cluster().OldVirtualIPSubnet != "" {
		dlog.Warningf(ctx, "please use routing.VirtualSubnet instead of deprecated deprecated cluster.VirtualIPSubnet")
		sn, err := netip.ParsePrefix(cfg.Cluster().OldVirtualIPSubnet)
		if err != nil {
			return nil, fmt.Errorf("unable to parse deprecated cluster.VirtualIPSubnet: %w", err)
		}
		cfg.Routing().VirtualSubnet = sn
	}
	return cfg, nil
}

// DestructiveMerge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *BaseConfig) DestructiveMerge(lc Config) {
	c.OSSpecificConfig.Merge(lc.OSSpecific())
	c.TimeoutsV.merge(lc.Timeouts())
	c.LogLevelsV.merge(lc.LogLevels())
	c.ImagesV.merge(lc.Images())
	c.GrpcV.merge(lc.Grpc())
	c.TelepresenceAPIV.merge(lc.TelepresenceAPI())
	c.InterceptV.merge(lc.Intercept())
	c.ClusterV.merge(lc.Cluster())
	c.DNSV.merge(lc.DNS())
	c.RoutingV.merge(lc.Routing())
}

func (c *BaseConfig) Merge(lc Config) Config {
	cfg := GetDefaultBaseConfig()
	*cfg = *c
	cfg.DestructiveMerge(lc)
	return cfg
}

func (c *BaseConfig) String() string {
	y, _ := c.MarshalYAML()
	return string(y)
}

// WatchConfig uses a file system watcher that receives events when the configuration changes
// and calls the given function when that happens.
func WatchConfig(c context.Context, onReload func(context.Context) error) error {
	configFile := GetConfigFile(c)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// The directory containing the config file must be watched because editing
	// the file will typically end with renaming the original and then creating
	// a new file. A watcher that follows the inode will not see when the new
	// file is created.
	if err = watcher.Add(filepath.Dir(configFile)); err != nil {
		return err
	}

	// The delay timer will initially sleep forever. It's reset to a very short
	// delay when the file is modified.
	delay := time.AfterFunc(time.Duration(math.MaxInt64), func() {
		cfg, err := LoadConfig(c)
		if err != nil {
			dlog.Error(c, err)
		} else {
			ReplaceConfig(c, cfg)
			err = onReload(c)
		}
		if err != nil {
			dlog.Error(c, err)
		}
	})
	defer delay.Stop()

	for {
		select {
		case <-c.Done():
			return nil
		case err = <-watcher.Errors:
			dlog.Error(c, err)
		case event := <-watcher.Events:
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 && event.Name == configFile {
				// The config file was created or modified. Let's defer the load just a little bit
				// in case there are more modifications (a write out with vi will typically cause
				// one CREATE event and at least one WRITE event).
				delay.Reset(5 * time.Millisecond)
			}
		}
	}
}

type Timeouts struct {
	// These all nave names starting with "Private" because we "want" them to be unexported in
	// order to force you to use .TimeoutContext(), but (1) we dont' want them to be hidden from
	// the JSON/YAML engines, and (2) in the rare case, we do want to be able to reach in and
	// grab it, but we want it to be clear that this is "bad".  We should probably (TODO) get
	// rid of those later cases, but let's not spend time doing that right now; and instead just
	// make them easy to grep for (`grep Private`) later.

	// PrivateClusterConnect is the maximum time to wait for a connection to the cluster to be established
	PrivateClusterConnect time.Duration `json:"clusterConnect"`
	// PrivateConnectivityCheck timeout used when checking if the cluster is already proxied on the workstation
	PrivateConnectivityCheck time.Duration `json:"connectivityCheck"`
	// PrivateEndpointDial is how long to wait for a Dial to a service for which the IP is known.
	PrivateEndpointDial time.Duration `json:"endpointDial"`
	// PrivateHelm is how long to wait for any helm operation.
	PrivateHelm time.Duration `json:"helm"`
	// PrivateIntercept is the time to wait for an intercept after the agents has been installed
	PrivateIntercept time.Duration `json:"intercept"`
	// PrivateRoundtripLatency is how much to add to the EndpointDial timeout when establishing a remote connection.
	PrivateRoundtripLatency time.Duration `json:"roundtripLatency"`
	// PrivateProxyDial is how long to wait for the proxy to establish an outbound connection
	PrivateProxyDial time.Duration `json:"proxyDial"`
	// PrivateTrafficManagerConnect is how long to wait for the traffic-manager API to connect
	PrivateTrafficManagerAPI time.Duration `json:"trafficManagerAPI"`
	// PrivateTrafficManagerConnect is how long to wait for the initial port-forwards to the traffic-manager
	PrivateTrafficManagerConnect time.Duration `json:"trafficManagerConnect"`
	// PrivateFtpReadWrite read/write timeout used by the fuseftp client.
	PrivateTrafficAgentArrival time.Duration `json:"trafficAgentArrival"`
	// PrivateFtpReadWrite read/write timeout used by the fuseftp client.
	PrivateFtpReadWrite time.Duration `json:"ftpReadWrite"`
	// PrivateFtpShutdown max time to wait for the fuseftp client to complete pending operations before forcing termination.
	PrivateFtpShutdown time.Duration `json:"ftpShutdown"`
	// PrivateContainerShutdown max time to wait for a docker container to stop before forcing termination.
	PrivateContainerShutdown time.Duration `json:"containerShutdown"`
}

type TimeoutID int

const (
	TimeoutClusterConnect TimeoutID = iota
	TimeoutConnectivityCheck
	TimeoutEndpointDial
	TimeoutHelm
	TimeoutIntercept
	TimeoutProxyDial
	TimeoutRoundtripLatency
	TimeoutTrafficManagerAPI
	TimeoutTrafficManagerConnect
	TimeoutTrafficAgentArrival
	TimeoutFtpReadWrite
	TimeoutFtpShutdown
	TimeoutContainerShutdown
)

type timeoutContext struct {
	context.Context
	timeoutID  TimeoutID
	timeoutVal time.Duration
}

func (ctx timeoutContext) Err() error {
	err := ctx.Context.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		err = timeoutError{
			timeoutID:  ctx.timeoutID,
			timeoutVal: ctx.timeoutVal,
			configFile: GetConfigFile(ctx),
			err:        err,
		}
	}
	return err
}

func (t *Timeouts) Get(timeoutID TimeoutID) time.Duration {
	var timeoutVal time.Duration
	switch timeoutID {
	case TimeoutClusterConnect:
		timeoutVal = t.PrivateClusterConnect
	case TimeoutConnectivityCheck:
		timeoutVal = t.PrivateConnectivityCheck
	case TimeoutEndpointDial:
		timeoutVal = t.PrivateEndpointDial
	case TimeoutHelm:
		timeoutVal = t.PrivateHelm
	case TimeoutIntercept:
		timeoutVal = t.PrivateIntercept
	case TimeoutProxyDial:
		timeoutVal = t.PrivateProxyDial
	case TimeoutRoundtripLatency:
		timeoutVal = t.PrivateRoundtripLatency
	case TimeoutTrafficManagerAPI:
		timeoutVal = t.PrivateTrafficManagerAPI
	case TimeoutTrafficManagerConnect:
		timeoutVal = t.PrivateTrafficManagerConnect
	case TimeoutTrafficAgentArrival:
		timeoutVal = t.PrivateTrafficAgentArrival
	case TimeoutFtpReadWrite:
		timeoutVal = t.PrivateFtpReadWrite
	case TimeoutFtpShutdown:
		timeoutVal = t.PrivateFtpShutdown
	case TimeoutContainerShutdown:
		timeoutVal = t.PrivateContainerShutdown
	default:
		panic("should not happen")
	}
	return timeoutVal
}

func (t *Timeouts) TimeoutContext(ctx context.Context, timeoutID TimeoutID) (context.Context, context.CancelFunc) {
	timeoutVal := t.Get(timeoutID)
	ctx, cancel := context.WithTimeout(ctx, timeoutVal)
	ctx = timeoutContext{
		Context:    ctx,
		timeoutID:  timeoutID,
		timeoutVal: timeoutVal,
	}
	return ctx, cancel
}

type timeoutError struct {
	timeoutID  TimeoutID
	timeoutVal time.Duration
	configFile string
	err        error
}

func (e timeoutError) Error() string {
	var yamlName, humanName string
	switch e.timeoutID {
	case TimeoutClusterConnect:
		yamlName = "clusterConnect"
		humanName = "cluster connect"
	case TimeoutConnectivityCheck:
		yamlName = "connectivityCheck"
		humanName = "connectivity check"
	case TimeoutEndpointDial:
		yamlName = "endpointDial"
		humanName = "tunnel endpoint dial with known IP"
	case TimeoutHelm:
		yamlName = "helm"
		humanName = "helm operation"
	case TimeoutIntercept:
		yamlName = "intercept"
		humanName = "intercept"
	case TimeoutProxyDial:
		yamlName = "proxyDial"
		humanName = "proxy dial"
	case TimeoutRoundtripLatency:
		yamlName = "roundtripDelay"
		humanName = "additional delay for tunnel roundtrip"
	case TimeoutTrafficManagerAPI:
		yamlName = "trafficManagerAPI"
		humanName = "traffic manager gRPC API"
	case TimeoutTrafficManagerConnect:
		yamlName = "trafficManagerConnect"
		humanName = "port-forward connection to the traffic manager"
	case TimeoutTrafficAgentArrival:
		yamlName = "trafficAgentArrival"
		humanName = "waiting for traffic agent arrival"
	case TimeoutFtpReadWrite:
		yamlName = "ftpReadWrite"
		humanName = "FTP client read/write"
	case TimeoutFtpShutdown:
		yamlName = "ftpShutdown"
		humanName = "FTP client shutdown grace period"
	case TimeoutContainerShutdown:
		yamlName = "containerShutdown"
		humanName = "Docker container shutdown grace period"
	default:
		panic("should not happen")
	}
	return fmt.Sprintf("the %s timed out.  The current timeout %s can be configured as %q in %q",
		humanName, e.timeoutVal, "timeouts."+yamlName, e.configFile)
}

func (e timeoutError) Unwrap() error {
	return e.err
}

func CheckTimeout(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && (errors.Is(ctxErr, context.DeadlineExceeded) || err == nil) {
		return ctxErr
	}
	return err
}

const (
	defaultTimeoutsClusterConnect        = 20 * time.Second
	defaultTimeoutsConnectivityCheck     = 500 * time.Millisecond
	defaultTimeoutsEndpointDial          = 3 * time.Second
	defaultTimeoutsHelm                  = 30 * time.Second
	defaultTimeoutsIntercept             = 30 * time.Second
	defaultTimeoutsProxyDial             = 5 * time.Second
	defaultTimeoutsRoundtripLatency      = 2 * time.Second
	defaultTimeoutsTrafficManagerAPI     = 15 * time.Second
	defaultTimeoutsTrafficManagerConnect = 60 * time.Second
	defaultTimeoutsTrafficAgentArrival   = 60 * time.Second
	defaultTimeoutsFtpReadWrite          = 1 * time.Minute
	defaultTimeoutsFtpShutdown           = 2 * time.Minute
	defaultTimeoutsContainerShutdown     = 0
	maxTimeoutsConnectivityCheck         = 5 * time.Second
)

var defaultTimeouts = Timeouts{ //nolint:gochecknoglobals // constant
	PrivateClusterConnect:        defaultTimeoutsClusterConnect,
	PrivateConnectivityCheck:     defaultTimeoutsConnectivityCheck,
	PrivateEndpointDial:          defaultTimeoutsEndpointDial,
	PrivateHelm:                  defaultTimeoutsHelm,
	PrivateIntercept:             defaultTimeoutsIntercept,
	PrivateProxyDial:             defaultTimeoutsProxyDial,
	PrivateRoundtripLatency:      defaultTimeoutsRoundtripLatency,
	PrivateTrafficManagerAPI:     defaultTimeoutsTrafficManagerAPI,
	PrivateTrafficManagerConnect: defaultTimeoutsTrafficManagerConnect,
	PrivateTrafficAgentArrival:   defaultTimeoutsTrafficAgentArrival,
	PrivateFtpReadWrite:          defaultTimeoutsFtpReadWrite,
	PrivateFtpShutdown:           defaultTimeoutsFtpShutdown,
	PrivateContainerShutdown:     defaultTimeoutsContainerShutdown,
}

func (t *Timeouts) defaults() DefaultsAware {
	return &defaultTimeouts
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (t *Timeouts) merge(o *Timeouts) {
	mergeNonDefaults(t, o)
	if t.PrivateConnectivityCheck > maxTimeoutsConnectivityCheck {
		t.PrivateConnectivityCheck = maxTimeoutsConnectivityCheck
	}
}

// IsZero controls whether this element will be included in marshalled output.
func (t *Timeouts) IsZero() bool {
	return t == nil || *t == defaultTimeouts
}

func (t *Timeouts) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(t), opts)
}

func (t *Timeouts) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Timeouts
	wp := (*wt)(t)
	return json.UnmarshalDecode(in, &wp, opts)
}

const (
	defaultLogLevelsUserDaemon = logrus.InfoLevel
	defaultLogLevelsRootDaemon = logrus.InfoLevel
)

var defaultLogLevels = LogLevels{ //nolint:gochecknoglobals // constant
	UserDaemon: defaultLogLevelsUserDaemon,
	RootDaemon: defaultLogLevelsRootDaemon,
}

type LogLevels struct {
	UserDaemon logrus.Level `json:"userDaemon"`
	RootDaemon logrus.Level `json:"rootDaemon"`
}

func (ll *LogLevels) defaults() DefaultsAware {
	return &defaultLogLevels
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (ll *LogLevels) merge(o *LogLevels) {
	mergeNonDefaults(ll, o)
}

// IsZero controls whether this element will be included in marshalled output.
func (ll *LogLevels) IsZero() bool {
	return ll == nil || *ll == defaultLogLevels
}

func (ll *LogLevels) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(ll), opts)
}

func (ll *LogLevels) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt LogLevels
	wp := (*wt)(ll)
	return json.UnmarshalDecode(in, &wp, opts)
}

type Images struct {
	PrivateRegistry        string `json:"registry"`
	PrivateAgentImage      string `json:"agentImage"`
	PrivateClientImage     string `json:"clientImage"`
	PrivateWebhookRegistry string `json:"webhookRegistry"`
}

const (
	defaultImagesRegistry = "ghcr.io/telepresenceio"
)

var defaultImages = Images{ //nolint:gochecknoglobals // constant
	PrivateRegistry: defaultImagesRegistry,
}

func (img *Images) defaults() DefaultsAware {
	return &defaultImages
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (img *Images) merge(o *Images) {
	mergeNonDefaults(img, o)
}

// IsZero controls whether this element will be included in marshalled output.
func (img *Images) IsZero() bool {
	return img == nil || *img == defaultImages
}

func (img *Images) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(img), opts)
}

func (img *Images) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Images
	wp := (*wt)(img)
	return json.UnmarshalDecode(in, &wp, opts)
}

func (img *Images) Registry(c context.Context) string {
	if img.PrivateRegistry == defaultImagesRegistry {
		env := GetEnv(c)
		if env.Registry != "" {
			return env.Registry
		}
	}
	return img.PrivateRegistry
}

func (img *Images) WebhookRegistry(_ context.Context) string {
	return img.PrivateWebhookRegistry
}

func (img *Images) AgentImage(c context.Context) string {
	if img.PrivateAgentImage != "" {
		return img.PrivateAgentImage
	}
	return GetEnv(c).AgentImage
}

func (img *Images) ClientImage(c context.Context) string {
	if img.PrivateClientImage != "" {
		return img.PrivateClientImage
	}
	return GetEnv(c).ClientImage
}

type Grpc struct {
	// MaxReceiveSize is the maximum message size in bytes the client can receive in a gRPC call or stream message.
	// Overrides the gRPC default of 4MB.
	MaxReceiveSizeV resource.Quantity `json:"maxReceiveSize"`
}

func (g *Grpc) MaxReceiveSize() int64 {
	if !g.MaxReceiveSizeV.IsZero() {
		if mz, ok := g.MaxReceiveSizeV.AsInt64(); ok {
			return mz
		}
	}
	return 0
}

func (g *Grpc) merge(o *Grpc) {
	if !o.MaxReceiveSizeV.IsZero() {
		g.MaxReceiveSizeV = o.MaxReceiveSizeV
	}
}

// IsZero controls whether this element will be included in marshalled output.
func (g *Grpc) IsZero() bool {
	return g == nil || g.MaxReceiveSizeV.IsZero()
}

type TelepresenceAPI struct {
	Port int `json:"port"`
}

func (g *TelepresenceAPI) merge(o *TelepresenceAPI) {
	if o.Port != 0 {
		g.Port = o.Port
	}
}

type Telemount DockerImage

var defaultTelemount = Telemount{ //nolint:gochecknoglobals // constant
	RegistryAPI: "ghcr.io/v2",
	Registry:    "ghcr.io",
	Namespace:   "telepresenceio",
	Repository:  "telemount",
}

func (tm *Telemount) defaults() DefaultsAware {
	return &defaultTelemount
}

func (tm *Telemount) IsZero() bool {
	return *tm == defaultTelemount
}

func (tm *Telemount) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(tm), opts)
}

func (tm *Telemount) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Telemount
	wp := (*wt)(tm)
	return json.UnmarshalDecode(in, &wp, opts)
}

var defaultIntercept = Intercept{ //nolint:gochecknoglobals // constant
	AppProtocolStrategy: k8sapi.Http2Probe,
	Telemount:           defaultTelemount,
}

type DockerImage struct {
	RegistryAPI string `json:"registryAPI"`
	Registry    string `json:"registry"`
	Namespace   string `json:"namespace"`
	Repository  string `json:"repository"`
	Tag         string `json:"tag"`
}

type Intercept struct {
	AppProtocolStrategy k8sapi.AppProtocolStrategy `json:"appProtocolStrategy"`
	DefaultPort         int                        `json:"defaultPort"`
	UseFtp              bool                       `json:"useFtp"`
	Telemount           Telemount                  `json:"telemount,omitzero"`
}

func (ic *Intercept) defaults() DefaultsAware {
	return &defaultIntercept
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (ic *Intercept) merge(o *Intercept) {
	mergeNonDefaults(ic, o)
}

// IsZero controls whether this element will be included in marshalled output.
func (ic *Intercept) IsZero() bool {
	return ic == nil || *ic == defaultIntercept
}

func (ic *Intercept) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(ic), opts)
}

func (ic *Intercept) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Intercept
	wp := (*wt)(ic)
	return json.UnmarshalDecode(in, &wp, opts)
}

type Cluster struct {
	DefaultManagerNamespace string   `json:"defaultManagerNamespace"`
	MappedNamespaces        []string `json:"mappedNamespaces"`
	ConnectFromRootDaemon   bool     `json:"connectFromRootDaemon"`
	ForceSPDY               bool     `json:"forceSPDY"`
	AgentPortForward        bool     `json:"agentPortForward"`

	// deprecated, use Routing.VirtualSubnet
	OldVirtualIPSubnet string `json:"virtualIPSubnet"`
}

// This is used by a different config -- the k8s_config, which needs to be able to tell if it's overridden at a cluster or environment variable level.
// Hence, we don't default to "ambassador" but to empty, so that it can check that no default has been given.
const defaultDefaultManagerNamespace = ""

var defaultCluster = Cluster{ //nolint:gochecknoglobals // constant
	DefaultManagerNamespace: defaultDefaultManagerNamespace,
	ConnectFromRootDaemon:   true,
	AgentPortForward:        true,
}

func (cc *Cluster) defaults() DefaultsAware {
	return &defaultCluster
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (cc *Cluster) merge(o *Cluster) {
	mergeNonDefaults(cc, o)
}

// IsZero controls whether this element will be included in marshalled output.
func (cc *Cluster) IsZero() bool {
	return cc == nil || isDefault(cc)
}

func (cc *Cluster) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(cc), opts)
}

func (cc *Cluster) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Cluster
	wp := (*wt)(cc)
	return json.UnmarshalDecode(in, &wp, opts)
}

type Routing struct {
	Subnets                []netip.Prefix `json:"subnets,omitempty"`
	AlsoProxy              []netip.Prefix `json:"alsoProxySubnets,omitempty"`
	NeverProxy             []netip.Prefix `json:"neverProxySubnets,omitempty"`
	AllowConflicting       []netip.Prefix `json:"allowConflictingSubnets,omitempty"`
	RecursionBlockDuration time.Duration  `json:"recursionBlockDuration,omitempty"`
	RecursionBlockTreads   int            `json:"recursionBlockTreads,omitempty"`
	VirtualSubnet          netip.Prefix   `json:"virtualSubnet"`
	AutoResolveConflicts   bool           `json:"autoResolveConflicts"`

	// For backward compatibility.
	OldAlsoProxy        []netip.Prefix `json:"alsoProxy,omitempty"`
	OldNeverProxy       []netip.Prefix `json:"neverProxy,omitempty"`
	OldAllowConflicting []netip.Prefix `json:"allowConflicting,omitempty"`
}

const (
	defaultAutoResolveConflicts  = true
	defaultRecursionBlockThreads = 5
)

var defaultRouting = Routing{ //nolint:gochecknoglobals // constant
	VirtualSubnet:        defaultVirtualSubnet,
	AutoResolveConflicts: defaultAutoResolveConflicts,
	RecursionBlockTreads: defaultRecursionBlockThreads,
}

func (r *Routing) defaults() DefaultsAware {
	return &defaultRouting
}

func (r *Routing) merge(o *Routing) {
	if len(o.AlsoProxy) > 0 {
		r.AlsoProxy = o.AlsoProxy
	} else if len(o.OldAlsoProxy) > 0 {
		r.AlsoProxy = o.OldAlsoProxy
	}
	if len(o.NeverProxy) > 0 {
		r.NeverProxy = o.NeverProxy
	} else if len(o.OldNeverProxy) > 0 {
		r.NeverProxy = o.OldNeverProxy
	}
	if len(o.AllowConflicting) > 0 {
		r.AllowConflicting = o.AllowConflicting
	} else if len(o.OldAllowConflicting) > 0 {
		r.AllowConflicting = o.OldAllowConflicting
	}
	if len(o.Subnets) > 0 {
		r.Subnets = o.Subnets
	}
	if o.RecursionBlockDuration > 0 {
		r.RecursionBlockDuration = o.RecursionBlockDuration
	}
	if o.RecursionBlockTreads != defaultRecursionBlockThreads {
		r.RecursionBlockTreads = o.RecursionBlockTreads
	}
	if o.VirtualSubnet != defaultVirtualSubnet {
		r.VirtualSubnet = o.VirtualSubnet
	}
	if o.AutoResolveConflicts != defaultAutoResolveConflicts { //nolint:gosimple // keep for the semantic clarity
		r.AutoResolveConflicts = o.AutoResolveConflicts
	}
}

// IsZero controls whether this element will be included in marshalled output.
func (r *Routing) IsZero() bool {
	return r == nil || isDefault(r)
}

func (r *Routing) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(r), opts)
}

func (r *Routing) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt Routing
	wp := (*wt)(r)
	return json.UnmarshalDecode(in, &wp, opts)
}

func (d *DNS) Equal(o *DNS) bool {
	if d == nil || o == nil {
		return d == o
	}
	return o.LocalIP == d.LocalIP &&
		o.RemoteIP == d.RemoteIP &&
		o.LookupTimeout == d.LookupTimeout &&
		slices.Equal(o.IncludeSuffixes, d.IncludeSuffixes) &&
		slices.Equal(o.ExcludeSuffixes, d.ExcludeSuffixes) &&
		slices.Equal(o.Excludes, d.Excludes) &&
		slices.Equal(o.Mappings, d.Mappings)
}

var DefaultExcludeSuffixes = []string{ //nolint:gochecknoglobals // constant
	".com",
	".io",
	".net",
	".org",
	".ru",
}

var defaultDNS = DNS{ //nolint:gochecknoglobals // constant
	ExcludeSuffixes: DefaultExcludeSuffixes,
}

func (d *DNS) defaults() DefaultsAware {
	return &defaultDNS
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (d *DNS) merge(o *DNS) {
	mergeNonDefaults(d, o)
}

// IsZero controls whether this element will be included in marshalled output.
func (d *DNS) IsZero() bool {
	return d == nil || d.Equal(&defaultDNS)
}

func (d *DNS) MarshalJSONTo(out *jsontext.Encoder, opts json.Options) error {
	return json.MarshalEncode(out, mapWithoutDefaults(d), opts)
}

func (d *DNS) UnmarshalJSONFrom(in *jsontext.Decoder, opts json.Options) error {
	// Prevent that the original object is cleared when an empty object is decoded by passing the address
	// of the pointer to the object. The unmarshal will then instead clear the pointer (wp becomes nil) and
	// leave the underlying object intact. In other words, this code achieves "omitempty" during unmarshal.
	type wt DNS
	wp := (*wt)(d)
	return json.UnmarshalDecode(in, &wp, opts)
}

type configKey struct{}

// WithConfig returns a context with the given Config.
func WithConfig(ctx context.Context, config Config) context.Context {
	pv := &config
	return context.WithValue(ctx, configKey{}, (*unsafe.Pointer)(unsafe.Pointer(&pv)))
}

func GetConfig(ctx context.Context) Config {
	if configPtr, ok := ctx.Value(configKey{}).(*unsafe.Pointer); ok {
		return *(*Config)(atomic.LoadPointer(configPtr))
	}
	panic("no Config has been set")
}

// ReplaceConfig replaces the config last stored using WithConfig with the given Config.
func ReplaceConfig(ctx context.Context, config Config) {
	if configPtr, ok := ctx.Value(configKey{}).(*unsafe.Pointer); ok {
		atomic.StorePointer(configPtr, unsafe.Pointer(&config))
	}
}

// GetConfigFile gets the path to the configFile as stored in filelocation.AppUserConfigDir.
func GetConfigFile(c context.Context) string {
	return filepath.Join(filelocation.AppUserConfigDir(c), ConfigFile)
}

//nolint:gochecknoglobals // extension point
var GetDefaultConfigFunc = func() Config {
	return GetDefaultBaseConfig()
}

//nolint:gochecknoglobals // extension point
var ValidateConfigFunc = func(context.Context, Config) error {
	return nil
}

// GetDefaultConfig returns the default configuration settings.
func GetDefaultConfig() Config {
	return GetDefaultConfigFunc()
}

var defaultConfig = BaseConfig{ //nolint:gochecknoglobals // constant
	OSSpecificConfig: GetDefaultOSSpecificConfig(),
	TimeoutsV:        defaultTimeouts,
	LogLevelsV:       defaultLogLevels,
	ImagesV:          defaultImages,
	GrpcV:            Grpc{},
	TelepresenceAPIV: TelepresenceAPI{},
	InterceptV:       defaultIntercept,
	ClusterV:         defaultCluster,
	DNSV:             defaultDNS,
	RoutingV:         defaultRouting,
}

// GetDefaultBaseConfig returns the default configuration settings.
func GetDefaultBaseConfig() *BaseConfig {
	c := new(BaseConfig)
	*c = defaultConfig
	return c
}

// LoadConfig loads and returns the Telepresence configuration as stored in filelocation.AppUserConfigDir
// or filelocation.AppSystemConfigDirs.
func LoadConfig(c context.Context) (cfg Config, err error) {
	defer func() {
		if err != nil {
			err = errcat.Config.New(err)
		}
	}()

	dirs := filelocation.AppSystemConfigDirs(c)
	cfg = GetDefaultConfigFunc()
	readMerge := func(dir string) error {
		if stat, err := os.Stat(dir); err != nil || !stat.IsDir() { // skip unless directory
			return nil
		}
		fileName := filepath.Join(dir, ConfigFile)
		bs, err := os.ReadFile(fileName)
		if err != nil {
			if os.IsNotExist(err) {
				err = nil
			}
			return err
		}
		fileConfig, err := ParseConfigYAML(c, fileName, bs)
		if err != nil {
			return err
		}
		cfg.DestructiveMerge(fileConfig)
		return nil
	}

	for _, dir := range dirs {
		if err = readMerge(dir); err != nil {
			return nil, err
		}
	}
	appDir := filelocation.AppUserConfigDir(c)
	if err = readMerge(appDir); err != nil {
		return nil, err
	}
	if err = ValidateConfigFunc(c, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// RoutingSnake is the same as Routing but with snake_case json/yaml names.
type RoutingSnake struct {
	Subnets                []netip.Prefix `json:"subnets"`
	AlsoProxy              []netip.Prefix `json:"also_proxy_subnets"`
	NeverProxy             []netip.Prefix `json:"never_proxy_subnets"`
	AllowConflicting       []netip.Prefix `json:"allow_conflicting_subnets"`
	RecursionBlockDuration time.Duration  `json:"recursion_block_duration"`
	VirtualSubnet          netip.Prefix   `json:"virtual_subnet"`
	AutoResolveConflicts   bool           `json:"auto_resolve_conflicts"`
}

type DNS struct {
	Error           string        `json:"error"`
	LocalIP         netip.Addr    `json:"localIP"`
	RemoteIP        netip.Addr    `json:"remoteIP"`
	IncludeSuffixes []string      `json:"includeSuffixes"`
	ExcludeSuffixes []string      `json:"excludeSuffixes"`
	Excludes        []string      `json:"excludes"`
	Mappings        DNSMappings   `json:"mappings"`
	LookupTimeout   time.Duration `json:"lookupTimeout"`
}

// DNSSnake is the same as DNS but with snake_case json/yaml names.
type DNSSnake struct {
	Error           string        `json:"error"`
	LocalIP         netip.Addr    `json:"local_ip"`
	RemoteIP        netip.Addr    `json:"remote_ip"`
	IncludeSuffixes []string      `json:"include_suffixes"`
	ExcludeSuffixes []string      `json:"exclude_suffixes"`
	Excludes        []string      `json:"excludes"`
	Mappings        DNSMappings   `json:"mappings"`
	LookupTimeout   time.Duration `json:"lookup_timeout"`
}

func (d *DNS) ToRPC() *daemon.DNSConfig {
	rd := daemon.DNSConfig{
		LocalIp:         d.LocalIP.AsSlice(),
		RemoteIp:        d.RemoteIP.AsSlice(),
		ExcludeSuffixes: d.ExcludeSuffixes,
		IncludeSuffixes: d.IncludeSuffixes,
		Excludes:        d.Excludes,
		LookupTimeout:   durationpb.New(d.LookupTimeout),
		Error:           d.Error,
	}
	if len(d.Mappings) > 0 {
		rd.Mappings = make([]*daemon.DNSMapping, len(d.Mappings))
		for i, n := range d.Mappings {
			rd.Mappings[i] = &daemon.DNSMapping{
				Name:     n.Name,
				AliasFor: n.AliasFor,
			}
		}
	}
	return &rd
}

func (d *DNS) ToSnake() *DNSSnake {
	return &DNSSnake{
		LocalIP:         d.LocalIP,
		RemoteIP:        d.RemoteIP,
		ExcludeSuffixes: d.ExcludeSuffixes,
		IncludeSuffixes: d.IncludeSuffixes,
		Excludes:        d.Excludes,
		Mappings:        d.Mappings,
		LookupTimeout:   d.LookupTimeout,
		Error:           d.Error,
	}
}

func MappingsFromRPC(mappings []*daemon.DNSMapping) DNSMappings {
	if l := len(mappings); l > 0 {
		ml := make(DNSMappings, l)
		for i, m := range mappings {
			ml[i] = &DNSMapping{
				Name:     m.Name,
				AliasFor: m.AliasFor,
			}
		}
		return ml
	}
	return nil
}

func DNSFromRPC(s *daemon.DNSConfig) *DNS {
	c := DNS{
		ExcludeSuffixes: s.ExcludeSuffixes,
		IncludeSuffixes: s.IncludeSuffixes,
		Excludes:        s.Excludes,
		Mappings:        MappingsFromRPC(s.Mappings),
		Error:           s.Error,
	}
	if ip, ok := netip.AddrFromSlice(s.LocalIp); ok {
		c.LocalIP = ip
	}
	if ip, ok := netip.AddrFromSlice(s.RemoteIp); ok {
		c.RemoteIP = ip
	}
	if s.LookupTimeout != nil {
		c.LookupTimeout = s.LookupTimeout.AsDuration()
	}
	return &c
}

func (r *Routing) ToSnake() *RoutingSnake {
	return &RoutingSnake{
		Subnets:              r.Subnets,
		AlsoProxy:            r.AlsoProxy,
		NeverProxy:           r.NeverProxy,
		AllowConflicting:     r.AllowConflicting,
		AutoResolveConflicts: r.AutoResolveConflicts,
	}
}

type SessionConfig struct {
	Config     `json:"clientConfig"`
	ClientFile string `json:"clientFile"`
}

func (sc *SessionConfig) UnmarshalJSON(data []byte) error {
	type tmpType SessionConfig
	var tmpJSON tmpType
	tmpJSON.Config = GetDefaultConfig()
	if err := UnmarshalJSON(data, &tmpJSON, false); err != nil {
		return err
	}
	*sc = SessionConfig(tmpJSON)
	return nil
}
