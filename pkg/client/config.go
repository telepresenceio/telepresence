package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const configFile = "config.yml"

// Config contains all configuration values for the telepresence CLI
type Config struct {
	Timeouts  Timeouts  `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`
	LogLevels LogLevels `json:"logLevels,omitempty" yaml:"logLevels,omitempty"`
	Images    Images    `json:"images,omitempty" yaml:"images,omitempty"`
	Cloud     Cloud     `json:"cloud,omitempty" yaml:"cloud,omitempty"`
	Grpc      Grpc      `json:"grpc,omitempty" yaml:"grpc,omitempty"`
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *Config) Merge(o *Config) {
	c.Timeouts.merge(&o.Timeouts)
	c.LogLevels.merge(&o.LogLevels)
	c.Images.merge(&o.Images)
	c.Cloud.merge(&o.Cloud)
	c.Grpc.merge(&o.Grpc)
}

func stringKey(n *yaml.Node) (string, error) {
	var s string
	if err := n.Decode(&s); err != nil {
		return "", errors.New(withLoc("key must be a string", n))
	}
	return s, nil
}

func (c *Config) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("config must be an object", node))
	}
	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		switch {
		case kv == "timeouts":
			err := ms[i+1].Decode(&c.Timeouts)
			if err != nil {
				return err
			}
		case kv == "logLevels":
			err := ms[i+1].Decode(&c.LogLevels)
			if err != nil {
				return err
			}
		case kv == "images":
			err := ms[i+1].Decode(&c.Images)
			if err != nil {
				return err
			}
		case kv == "cloud":
			err := ms[i+1].Decode(&c.Cloud)
			if err != nil {
				return err
			}
		case kv == "grpc":
			err := ms[i+1].Decode(&c.Grpc)
			if err != nil {
				return err
			}
		case parseContext != nil:
			dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
		}
	}
	return nil
}

type Timeouts struct {
	// These all nave names starting with "Private" because we "want" them to be unexported in
	// order to force you to use .TimeoutContext(), but (1) we dont' want them to be hidden from
	// the JSON/YAML engines, and (2) in the rare case, we do want to be able to reach in and
	// grab it, but we want it to be clear that this is "bad".  We should probably (TODO) get
	// rid of those later cases, but let's not spend time doing that right now; and instead just
	// make them easy to grep for (`grep Private`) later.

	// PrivateAgentInstall is how long to wait for an agent to be installed (i.e. apply of service and deploy manifests)
	PrivateAgentInstall time.Duration `json:"agentInstall,omitempty" yaml:"agentInstall,omitempty"`
	// PrivateApply is how long to wait for a k8s manifest to be applied
	PrivateApply time.Duration `json:"apply,omitempty" yaml:"apply,omitempty"`
	// PrivateClusterConnect is the maximum time to wait for a connection to the cluster to be established
	PrivateClusterConnect time.Duration `json:"clusterConnect,omitempty" yaml:"clusterConnect,omitempty"`
	// PrivateEndpointDial is how long to wait for a Dial to a service for which the IP is known.
	PrivateEndpointDial time.Duration `json:"endpointDial,omitempty" yaml:"endpointDial,omitempty"`
	// PrivateHelm is how long to wait for any helm operation.
	PrivateHelm time.Duration `json:"helm,omitempty" yaml:"helm,omitempty"`
	// PrivateIntercept is the time to wait for an intercept after the agents has been installed
	PrivateIntercept time.Duration `json:"intercept,omitempty" yaml:"intercept,omitempty"`
	// PrivateRoundtripLatency is how much to add  to the EndpointDial timeout when establishing a remote connection.
	PrivateRoundtripLatency time.Duration `json:"roundtripLatency,omitempty" yaml:"roundtripLatency,omitempty"`
	// PrivateProxyDial is how long to wait for the proxy to establish an outbound connection
	PrivateProxyDial time.Duration `json:"proxyDial,omitempty" yaml:"proxyDial,omitempty"`
	// PrivateTrafficManagerConnect is how long to wait for the traffic-manager API to connect
	PrivateTrafficManagerAPI time.Duration `json:"trafficManagerAPI,omitempty" yaml:"trafficManagerAPI,omitempty"`
	// PrivateTrafficManagerConnect is how long to wait for the initial port-forwards to the traffic-manager
	PrivateTrafficManagerConnect time.Duration `json:"trafficManagerConnect,omitempty" yaml:"trafficManagerConnect,omitempty"`
}

type TimeoutID int

const (
	TimeoutAgentInstall TimeoutID = iota
	TimeoutApply
	TimeoutClusterConnect
	TimeoutEndpointDial
	TimeoutHelm
	TimeoutIntercept
	TimeoutProxyDial
	TimeoutRoundtripLatency
	TimeoutTrafficManagerAPI
	TimeoutTrafficManagerConnect
)

type timeoutContext struct {
	context.Context
	timeoutID  TimeoutID
	timeoutVal time.Duration
}

func (ctx timeoutContext) Err() error {
	err := ctx.Context.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		err = timeoutErr{
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
	case TimeoutAgentInstall:
		timeoutVal = t.PrivateAgentInstall
	case TimeoutApply:
		timeoutVal = t.PrivateApply
	case TimeoutClusterConnect:
		timeoutVal = t.PrivateClusterConnect
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

type timeoutErr struct {
	timeoutID  TimeoutID
	timeoutVal time.Duration
	configFile string
	err        error
}

func (e timeoutErr) Error() string {
	var yamlName, humanName string
	switch e.timeoutID {
	case TimeoutAgentInstall:
		yamlName = "agentInstall"
		humanName = "agent install"
	case TimeoutApply:
		yamlName = "apply"
		humanName = "apply"
	case TimeoutClusterConnect:
		yamlName = "clusterConnect"
		humanName = "cluster connect"
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
	default:
		panic("should not happen")
	}
	return fmt.Sprintf("the %s timed out.  The current timeout %s can be configured as %q in %q",
		humanName, e.timeoutVal, "timeouts."+yamlName, e.configFile)
}

func (e timeoutErr) Unwrap() error {
	return e.err
}

func CheckTimeout(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && (errors.Is(ctxErr, context.DeadlineExceeded) || err == nil) {
		return ctxErr
	}
	return err
}

// UnmarshalYAML caters for the unfortunate fact that time.Duration doesn't do YAML or JSON at all.
func (t *Timeouts) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("timeouts must be an object", node))
	}
	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		var dp *time.Duration
		switch kv {
		case "agentInstall":
			dp = &t.PrivateAgentInstall
		case "apply":
			dp = &t.PrivateApply
		case "clusterConnect":
			dp = &t.PrivateClusterConnect
		case "endpointDial":
			dp = &t.PrivateEndpointDial
		case "helm":
			dp = &t.PrivateHelm
		case "intercept":
			dp = &t.PrivateIntercept
		case "proxyDial":
			dp = &t.PrivateProxyDial
		case "roundtripLatency":
			dp = &t.PrivateRoundtripLatency
		case "trafficManagerAPI":
			dp = &t.PrivateTrafficManagerAPI
		case "trafficManagerConnect":
			dp = &t.PrivateTrafficManagerConnect
		default:
			if parseContext != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
			}
			continue
		}

		v := ms[i+1]
		var vv interface{}
		if err = v.Decode(&vv); err != nil {
			return errors.New(withLoc("unable to parse value", v))
		}
		switch vv := vv.(type) {
		case int:
			*dp = time.Duration(vv) * time.Second
		case float64:
			*dp = time.Duration(vv * float64(time.Second))
		case string:
			if *dp, err = time.ParseDuration(vv); err != nil {
				return errors.New(withLoc(fmt.Sprintf("%q is not a valid duration", vv), v))
			}
		}
	}
	return nil
}

const defaultTimeoutsAgentInstall = 120 * time.Second
const defaultTimeoutsApply = 1 * time.Minute
const defaultTimeoutsClusterConnect = 20 * time.Second
const defaultTimeoutsEndpointDial = 3 * time.Second
const defaultTimeoutsHelm = 30 * time.Second
const defaultTimeoutsIntercept = 5 * time.Second
const defaultTimeoutsProxyDial = 5 * time.Second
const defaultTimeoutsRoundtripLatency = 2 * time.Second
const defaultTimeoutsTrafficManagerAPI = 15 * time.Second
const defaultTimeoutsTrafficManagerConnect = 60 * time.Second

// MarshalYAML is not using pointer receiver here, because Timeouts is not pointer in the Config struct
func (t Timeouts) MarshalYAML() (interface{}, error) {
	tm := make(map[string]string)
	if t.PrivateAgentInstall != 0 && t.PrivateAgentInstall != defaultTimeoutsAgentInstall {
		tm["agentInstall"] = t.PrivateAgentInstall.String()
	}
	if t.PrivateApply != 0 && t.PrivateApply != defaultTimeoutsApply {
		tm["apply"] = t.PrivateApply.String()
	}
	if t.PrivateClusterConnect != 0 && t.PrivateClusterConnect != defaultTimeoutsClusterConnect {
		tm["clusterConnect"] = t.PrivateClusterConnect.String()
	}
	if t.PrivateEndpointDial != 0 && t.PrivateEndpointDial != defaultTimeoutsEndpointDial {
		tm["endpointDial"] = t.PrivateEndpointDial.String()
	}
	if t.PrivateHelm != 0 && t.PrivateHelm != defaultTimeoutsHelm {
		tm["helm"] = t.PrivateHelm.String()
	}
	if t.PrivateIntercept != 0 && t.PrivateIntercept != defaultTimeoutsIntercept {
		tm["intercept"] = t.PrivateIntercept.String()
	}
	if t.PrivateProxyDial != 0 && t.PrivateProxyDial != defaultTimeoutsProxyDial {
		tm["proxyDial"] = t.PrivateProxyDial.String()
	}
	if t.PrivateRoundtripLatency != 0 && t.PrivateRoundtripLatency != defaultTimeoutsRoundtripLatency {
		tm["roundtripLatency"] = t.PrivateRoundtripLatency.String()
	}
	if t.PrivateTrafficManagerAPI != 0 && t.PrivateTrafficManagerAPI != defaultTimeoutsTrafficManagerAPI {
		tm["trafficManagerAPI"] = t.PrivateTrafficManagerAPI.String()
	}
	if t.PrivateTrafficManagerConnect != 0 && t.PrivateTrafficManagerConnect != defaultTimeoutsTrafficManagerConnect {
		tm["trafficManagerConnect"] = t.PrivateTrafficManagerConnect.String()
	}
	return tm, nil
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (t *Timeouts) merge(o *Timeouts) {
	if o.PrivateAgentInstall != 0 {
		t.PrivateAgentInstall = o.PrivateAgentInstall
	}
	if o.PrivateApply != 0 {
		t.PrivateApply = o.PrivateApply
	}
	if o.PrivateClusterConnect != 0 {
		t.PrivateClusterConnect = o.PrivateClusterConnect
	}
	if o.PrivateEndpointDial != 0 {
		t.PrivateEndpointDial = o.PrivateEndpointDial
	}
	if o.PrivateHelm != 0 {
		t.PrivateHelm = o.PrivateHelm
	}
	if o.PrivateIntercept != 0 {
		t.PrivateIntercept = o.PrivateIntercept
	}
	if o.PrivateProxyDial != 0 {
		t.PrivateProxyDial = o.PrivateProxyDial
	}
	if o.PrivateRoundtripLatency != 0 {
		t.PrivateRoundtripLatency = o.PrivateRoundtripLatency
	}
	if o.PrivateTrafficManagerAPI != 0 {
		t.PrivateTrafficManagerAPI = o.PrivateTrafficManagerAPI
	}
	if o.PrivateTrafficManagerConnect != 0 {
		t.PrivateTrafficManagerConnect = o.PrivateTrafficManagerConnect
	}
}

type LogLevels struct {
	UserDaemon logrus.Level `json:"userDaemon,omitempty" yaml:"userDaemon,omitempty"`
	RootDaemon logrus.Level `json:"rootDaemon,omitempty" yaml:"rootDaemon,omitempty"`
}

// UnmarshalYAML parses the logrus log-levels
func (ll *LogLevels) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("timeouts must be an object", node))
	}

	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		v := ms[i+1]
		level, err := logrus.ParseLevel(v.Value)
		if err != nil {
			return errors.New(withLoc("invalid log-level", v))
		}
		switch kv {
		case "userDaemon":
			ll.UserDaemon = level
		case "rootDaemon":
			ll.RootDaemon = level
		default:
			if parseContext != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
			}
		}
	}
	return nil
}

func (ll *LogLevels) merge(o *LogLevels) {
	if o.UserDaemon != 0 {
		ll.UserDaemon = o.UserDaemon
	}
	if o.RootDaemon != 0 {
		ll.RootDaemon = o.RootDaemon
	}
}

type Images struct {
	Registry          string `json:"registry,omitempty" yaml:"registry,omitempty"`
	AgentImage        string `json:"agentImage,omitempty" yaml:"agentImage,omitempty"`
	WebhookRegistry   string `json:"webhookRegistry,omitempty" yaml:"webhookRegistry,omitempty"`
	WebhookAgentImage string `json:"webhookAgentImage,omitempty" yaml:"webhookAgentImage,omitempty"`
}

// UnmarshalYAML parses the images YAML
func (img *Images) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("images must be an object", node))
	}

	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		v := ms[i+1]
		switch kv {
		case "registry":
			img.Registry = v.Value
		case "agentImage":
			img.AgentImage = v.Value
		case "webhookRegistry":
			img.WebhookRegistry = v.Value
		case "webhookAgentImage":
			img.WebhookAgentImage = v.Value
		default:
			if parseContext != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
			}
		}
	}
	return nil
}

func (i *Images) merge(o *Images) {
	if o.AgentImage != "" {
		i.AgentImage = o.AgentImage
	}
	if o.WebhookAgentImage != "" {
		i.WebhookAgentImage = o.WebhookAgentImage
	}
	if o.Registry != "" {
		i.Registry = o.Registry
	}
	if o.WebhookRegistry != "" {
		i.WebhookRegistry = o.WebhookRegistry
	}
}

type Cloud struct {
	SkipLogin       bool          `json:"skipLogin,omitempty" yaml:"skipLogin,omitempty"`
	RefreshMessages time.Duration `json:"refreshMessages,omitempty" yaml:"refreshMessages,omitempty"`
	SystemaHost     string        `json:"systemaHost,omitempty" yaml:"systemaHost,omitempty"`
	SystemaPort     string        `json:"systemaPort,omitempty" yaml:"systemaPort,omitempty"`
}

// UnmarshalYAML parses the images YAML
func (cloud *Cloud) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("cloud must be an object", node))
	}

	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		v := ms[i+1]
		switch kv {
		case "skipLogin":
			val, err := strconv.ParseBool(v.Value)
			if err != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("bool expected for key %q", kv), ms[i]))
			} else {
				cloud.SkipLogin = val
			}
		case "refreshMessages":
			duration, err := time.ParseDuration(v.Value)
			if err != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("duration expected for key %q", kv), ms[i]))
			} else {
				cloud.RefreshMessages = duration
			}
		case "systemaHost":
			cloud.SystemaHost = v.Value
		case "systemaPort":
			cloud.SystemaPort = v.Value
		default:
			if parseContext != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
			}
		}
	}
	return nil
}

const defaultCloudSystemAHost = "app.getambassador.io"
const defaultCloudSystemAPort = "443"
const defaultCloudRefreshMessages = 24 * 7 * time.Hour

// MarshalYAML is not using pointer receiver here, because Cloud is not pointer in the Config struct
func (cloud Cloud) MarshalYAML() (interface{}, error) {
	cm := make(map[string]interface{})
	if cloud.RefreshMessages != 0 && cloud.RefreshMessages != defaultCloudRefreshMessages {
		cm["refreshMessages"] = cloud.RefreshMessages.String()
	}
	if cloud.SkipLogin {
		cm["skipLogin"] = true
	}
	if cloud.SystemaHost != "" && cloud.SystemaHost != defaultCloudSystemAHost {
		cm["systemaHost"] = cloud.SystemaHost
	}
	if cloud.SystemaPort != "" && cloud.SystemaPort != defaultCloudSystemAPort {
		cm["systemaPort"] = cloud.SystemaPort
	}
	return cm, nil
}

func (cloud *Cloud) merge(o *Cloud) {
	if o.SkipLogin {
		cloud.SkipLogin = o.SkipLogin
	}
	if o.RefreshMessages != 0 {
		cloud.RefreshMessages = o.RefreshMessages
	}
	if o.SystemaHost != "" {
		cloud.SystemaHost = o.SystemaHost
	}
	if o.SystemaPort != "" {
		cloud.SystemaPort = o.SystemaPort
	}
}

type Grpc struct {
	// MaxReceiveSize is the maximum message size in bytes the client can receive in a gRPC call or stream message.
	// Overrides the gRPC default of 4MB.
	MaxReceiveSize *resource.Quantity `json:"maxReceiveSize,omitempty" yaml:"maxReceiveSize,omitempty"`
}

func (g *Grpc) merge(o *Grpc) {
	if o.MaxReceiveSize != nil {
		g.MaxReceiveSize = o.MaxReceiveSize
	}
}

// UnmarshalYAML parses the images YAML
func (g *Grpc) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(withLoc("grpc must be an object", node))
	}

	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := stringKey(ms[i])
		if err != nil {
			return err
		}
		v := ms[i+1]
		switch kv {
		case "maxReceiveSize":
			val, err := resource.ParseQuantity(v.Value)
			if err != nil {
				dlog.Warningf(parseContext, "unable to parse quantity %q: %v", v.Value, withLoc(err.Error(), ms[i]))
			} else {
				g.MaxReceiveSize = &val
			}
		default:
			if parseContext != nil {
				dlog.Warn(parseContext, withLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
			}
		}
	}
	return nil
}

// MarshalYAML is not using pointer receiver here, because Cloud is not pointer in the Config struct
func (g Grpc) MarshalYAML() (interface{}, error) {
	cm := make(map[string]interface{})
	if g.MaxReceiveSize != nil {
		cm["maxReceiveSize"] = g.MaxReceiveSize.String()
	}
	return cm, nil
}

var parseContext context.Context

type parsedFile struct{}

func withLoc(s string, n *yaml.Node) string {
	if parseContext != nil {
		if fileName, ok := parseContext.Value(parsedFile{}).(string); ok {
			return fmt.Sprintf("file %s, line %d: %s", fileName, n.Line, s)
		}
	}
	return fmt.Sprintf("line %d: %s", n.Line, s)
}

type configKey struct{}

// WithConfig returns a context with the given Config
func WithConfig(ctx context.Context, config *Config) context.Context {
	return context.WithValue(ctx, configKey{}, config)
}

func GetConfig(ctx context.Context) *Config {
	config, ok := ctx.Value(configKey{}).(*Config)
	if !ok {
		return nil
	}
	return config
}

// GetConfigFile gets the path to the configFile as stored in filelocation.AppUserConfigDir
func GetConfigFile(c context.Context) string {
	dir, _ := filelocation.AppUserConfigDir(c)
	return filepath.Join(dir, configFile)
}

// GetDefaultConfig returns the default configuration settings
func GetDefaultConfig(c context.Context) Config {
	cfg := Config{
		Timeouts: Timeouts{
			PrivateAgentInstall:          defaultTimeoutsAgentInstall,
			PrivateApply:                 defaultTimeoutsApply,
			PrivateClusterConnect:        defaultTimeoutsClusterConnect,
			PrivateEndpointDial:          defaultTimeoutsEndpointDial,
			PrivateHelm:                  defaultTimeoutsHelm,
			PrivateIntercept:             defaultTimeoutsIntercept,
			PrivateProxyDial:             defaultTimeoutsProxyDial,
			PrivateRoundtripLatency:      defaultTimeoutsRoundtripLatency,
			PrivateTrafficManagerAPI:     defaultTimeoutsTrafficManagerAPI,
			PrivateTrafficManagerConnect: defaultTimeoutsTrafficManagerConnect,
		},
		LogLevels: LogLevels{
			UserDaemon: logrus.InfoLevel,
			RootDaemon: logrus.InfoLevel,
		},
		Cloud: Cloud{
			SkipLogin:       false,
			RefreshMessages: defaultCloudRefreshMessages,
			SystemaHost:     defaultCloudSystemAHost,
			SystemaPort:     defaultCloudSystemAPort,
		},
		Grpc: Grpc{},
	}
	env := GetEnv(c)
	cfg.Images.Registry = env.Registry
	cfg.Images.WebhookRegistry = env.Registry
	cfg.Images.AgentImage = env.AgentImage
	cfg.Images.WebhookAgentImage = env.AgentImage
	return cfg
}

// LoadConfig loads and returns the Telepresence configuration as stored in filelocation.AppUserConfigDir
// or filelocation.AppSystemConfigDirs
func LoadConfig(c context.Context) (cfg *Config, err error) {
	defer func() {
		if err != nil {
			err = errcat.Config.New(err)
		}
	}()

	var dirs []string
	dirs, err = filelocation.AppSystemConfigDirs(c)
	if err != nil {
		return nil, err
	}

	dflt := GetDefaultConfig(c)
	cfg = &dflt
	readMerge := func(dir string) error {
		if stat, err := os.Stat(dir); err != nil || !stat.IsDir() { // skip unless directory
			return nil
		}
		fileName := filepath.Join(dir, configFile)
		bs, err := os.ReadFile(fileName)
		if err != nil {
			if os.IsNotExist(err) {
				err = nil
			}
			return err
		}
		parseContext = context.WithValue(c, parsedFile{}, fileName)
		defer func() {
			parseContext = nil
		}()
		fileConfig := Config{}
		if err = yaml.Unmarshal(bs, &fileConfig); err != nil {
			return err
		}
		cfg.Merge(&fileConfig)
		return nil
	}

	for _, dir := range dirs {
		if err = readMerge(dir); err != nil {
			return nil, err
		}
	}
	appDir, err := filelocation.AppUserConfigDir(c)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err = readMerge(appDir); err != nil {
		return nil, err
	}

	// Sanity check
	if os.Getenv("SYSTEMA_ENV") == "staging" && cfg.Cloud.SystemaHost != "beta-app.datawire.io" {
		return nil, errors.New("cloud.SystemaHost must be set to beta-app.datawire.io when using SYSTEMA_ENV set to 'staging'")
	}

	return cfg, nil
}
