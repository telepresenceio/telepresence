package client

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const configFile = "config.yml"

// Config contains all configuration values for the telepresence CLI
type Config struct {
	Timeouts  Timeouts  `json:"timeouts,omitempty"`
	LogLevels LogLevels `json:"logLevels,omitempty"`
	Images    Images    `json:"images,omitempty"`
	Cloud     Cloud     `json:"cloud,omitempty"`
	Grpc      Grpc      `json:"grpc,omitempty"`
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *Config) merge(o *Config) {
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
	PrivateAgentInstall time.Duration `json:"agentInstall,omitempty"`
	// PrivateApply is how long to wait for a k8s manifest to be applied
	PrivateApply time.Duration `json:"apply,omitempty"`
	// PrivateClusterConnect is the maximum time to wait for a connection to the cluster to be established
	PrivateClusterConnect time.Duration `json:"clusterConnect,omitempty"`
	// PrivateIntercept is the time to wait for an intercept after the agents has been installed
	PrivateIntercept time.Duration `json:"intercept,omitempty"`
	// PrivateProxyDial is how long to wait for the proxy to establish an outbound connection
	PrivateProxyDial time.Duration `json:"proxyDial,omitempty"`
	// PrivateTrafficManagerConnect is how long to wait for the traffic-manager API to connect
	PrivateTrafficManagerAPI time.Duration `json:"trafficManagerAPI,omitempty"`
	// PrivateTrafficManagerConnect is how long to wait for the initial port-forwards to the traffic-manager
	PrivateTrafficManagerConnect time.Duration `json:"trafficManagerConnect,omitempty"`
	// PrivateHelm is how long to wait for any helm operation.
	PrivateHelm time.Duration `json:"helm,omitempty"`
}

type TimeoutID int

const (
	TimeoutAgentInstall TimeoutID = iota
	TimeoutApply
	TimeoutClusterConnect
	TimeoutIntercept
	TimeoutProxyDial
	TimeoutTrafficManagerAPI
	TimeoutTrafficManagerConnect
	TimeoutHelm
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
	case TimeoutIntercept:
		timeoutVal = t.PrivateIntercept
	case TimeoutProxyDial:
		timeoutVal = t.PrivateProxyDial
	case TimeoutTrafficManagerAPI:
		timeoutVal = t.PrivateTrafficManagerAPI
	case TimeoutTrafficManagerConnect:
		timeoutVal = t.PrivateTrafficManagerConnect
	case TimeoutHelm:
		timeoutVal = t.PrivateHelm
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
	case TimeoutIntercept:
		yamlName = "intercept"
		humanName = "intercept"
	case TimeoutProxyDial:
		yamlName = "proxyDial"
		humanName = "proxy dial"
	case TimeoutTrafficManagerAPI:
		yamlName = "trafficManagerAPI"
		humanName = "traffic manager gRPC API"
	case TimeoutTrafficManagerConnect:
		yamlName = "trafficManagerConnect"
		humanName = "port-forward connection to the traffic manager"
	case TimeoutHelm:
		yamlName = "helm"
		humanName = "helm operation"
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
		case "intercept":
			dp = &t.PrivateIntercept
		case "proxyDial":
			dp = &t.PrivateProxyDial
		case "trafficManagerAPI":
			dp = &t.PrivateTrafficManagerAPI
		case "trafficManagerConnect":
			dp = &t.PrivateTrafficManagerConnect
		case "helm":
			dp = &t.PrivateHelm
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
	if o.PrivateIntercept != 0 {
		t.PrivateIntercept = o.PrivateIntercept
	}
	if o.PrivateProxyDial != 0 {
		t.PrivateProxyDial = o.PrivateProxyDial
	}
	if o.PrivateTrafficManagerAPI != 0 {
		t.PrivateTrafficManagerAPI = o.PrivateTrafficManagerAPI
	}
	if o.PrivateTrafficManagerConnect != 0 {
		t.PrivateTrafficManagerConnect = o.PrivateTrafficManagerConnect
	}
	if o.PrivateHelm != 0 {
		t.PrivateHelm = o.PrivateHelm
	}
}

type LogLevels struct {
	UserDaemon logrus.Level `json:"userDaemon,omitempty"`
	RootDaemon logrus.Level `json:"rootDaemon,omitempty"`
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
	Registry          string `json:"registry,omitempty"`
	AgentImage        string `json:"agentImage,omitempty"`
	WebhookRegistry   string `json:"webhookRegistry,omitempty"`
	WebhookAgentImage string `json:"webhookAgentImage,omitempty"`
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
	SkipLogin       bool          `json:"skipLogin,omitempty"`
	RefreshMessages time.Duration `json:"refreshMessages,omitempty"`
	SystemaHost     string        `json:"systemaHost,omitempty"`
	SystemaPort     string        `json:"systemaPort,omitempty"`
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

func (i *Cloud) merge(o *Cloud) {
	if o.SkipLogin {
		i.SkipLogin = o.SkipLogin
	}
	if o.RefreshMessages != 0 {
		i.RefreshMessages = o.RefreshMessages
	}
	if o.SystemaHost != "" {
		i.SystemaHost = o.SystemaHost
	}
	if o.SystemaPort != "" {
		i.SystemaPort = o.SystemaPort
	}
}

type Grpc struct {
	// MaxReceiveSize is the maximum message size in bytes the client can receive in a gRPC call or stream message.
	// Overrides the gRPC default of 4MB.
	MaxReceiveSize *resource.Quantity `json:"maxReceiveSize,omitempty"`
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

var defaultConfig = Config{
	Timeouts: Timeouts{
		PrivateAgentInstall:          120 * time.Second,
		PrivateApply:                 1 * time.Minute,
		PrivateClusterConnect:        20 * time.Second,
		PrivateIntercept:             5 * time.Second,
		PrivateProxyDial:             5 * time.Second,
		PrivateTrafficManagerAPI:     15 * time.Second,
		PrivateTrafficManagerConnect: 60 * time.Second,
		PrivateHelm:                  12 * time.Second,
	},
	LogLevels: LogLevels{
		UserDaemon: logrus.DebugLevel,
		RootDaemon: logrus.InfoLevel,
	},
	Images: Images{
		Registry:          "docker.io/datawire",
		WebhookRegistry:   "docker.io/datawire",
		AgentImage:        "",
		WebhookAgentImage: "",
	},
	Cloud: Cloud{
		SkipLogin:       false,
		RefreshMessages: 24 * 7 * time.Hour,
		SystemaHost:     "app.getambassador.io",
		SystemaPort:     "443",
	},
	Grpc: Grpc{},
}

var config *Config

var configOnce = new(sync.Once)

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

// GetConfig returns the Telepresence configuration as stored in filelocation.AppUserConfigDir
// or filelocation.AppSystemConfigDirs
//
func GetConfig(c context.Context) *Config {
	configOnce.Do(func() {
		var err error
		config, err = loadConfig(c)
		if err != nil {
			config = &defaultConfig
		}
	})
	return config
}

// GetConfigFile gets the path to the configFile as stored in filelocation.AppUserConfigDir
func GetConfigFile(c context.Context) string {
	dir, _ := filelocation.AppUserConfigDir(c)
	return filepath.Join(dir, configFile)
}

func loadConfig(c context.Context) (*Config, error) {
	dirs, err := filelocation.AppSystemConfigDirs(c)
	if err != nil {
		return nil, err
	}

	cfg := defaultConfig // start with a by value copy of the default

	readMerge := func(dir string) error {
		if stat, err := os.Stat(dir); err != nil || !stat.IsDir() { // skip unless directory
			return nil
		}
		fileName := filepath.Join(dir, configFile)
		bs, err := ioutil.ReadFile(fileName)
		if err != nil {
			if err == os.ErrNotExist {
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
		cfg.merge(&fileConfig)
		return nil
	}

	for _, dir := range dirs {
		if err = readMerge(dir); err != nil {
			return nil, err
		}
	}
	appDir, err := filelocation.AppUserConfigDir(c)
	if err != nil {
		return nil, err
	}
	if err = readMerge(appDir); err != nil {
		return nil, err
	}
	return &cfg, nil
}
