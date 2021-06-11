package client

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"gopkg.in/yaml.v3"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const configFile = "config.yml"

type Config struct {
	Timeouts  Timeouts  `json:"timeouts,omitempty"`
	LogLevels LogLevels `json:"logLevels,omitempty"`
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *Config) merge(o *Config) {
	c.Timeouts.merge(&o.Timeouts)
	c.LogLevels.merge(&o.LogLevels)
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
		if kv == "timeouts" {
			err := ms[i+1].Decode(&c.Timeouts)
			if err != nil {
				return err
			}
			continue
		}
		if kv == "logLevels" {
			err := ms[i+1].Decode(&c.LogLevels)
			if err != nil {
				return err
			}
			continue
		}
		if parseContext != nil {
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
)

type timeoutContext struct {
	context.Context
	timeoutID  TimeoutID
	timeoutVal time.Duration
}

func (ctx timeoutContext) Err() error {
	err := ctx.Context.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		dir, _ := filelocation.AppUserConfigDir(ctx)
		err = timeoutErr{
			timeoutID:  ctx.timeoutID,
			timeoutVal: ctx.timeoutVal,
			configFile: filepath.Join(dir, configFile),
			err:        err,
		}
	}
	return err
}

func (cfg Timeouts) TimeoutContext(ctx context.Context, timeoutID TimeoutID) (context.Context, context.CancelFunc) {
	var timeoutVal time.Duration
	switch timeoutID {
	case TimeoutAgentInstall:
		timeoutVal = cfg.PrivateAgentInstall
	case TimeoutApply:
		timeoutVal = cfg.PrivateApply
	case TimeoutClusterConnect:
		timeoutVal = cfg.PrivateClusterConnect
	case TimeoutIntercept:
		timeoutVal = cfg.PrivateIntercept
	case TimeoutProxyDial:
		timeoutVal = cfg.PrivateProxyDial
	case TimeoutTrafficManagerAPI:
		timeoutVal = cfg.PrivateTrafficManagerAPI
	case TimeoutTrafficManagerConnect:
		timeoutVal = cfg.PrivateTrafficManagerConnect
	default:
		panic("should not happen")
	}
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
func (d *Timeouts) UnmarshalYAML(node *yaml.Node) (err error) {
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
			dp = &d.PrivateAgentInstall
		case "apply":
			dp = &d.PrivateApply
		case "clusterConnect":
			dp = &d.PrivateClusterConnect
		case "intercept":
			dp = &d.PrivateIntercept
		case "proxyDial":
			dp = &d.PrivateProxyDial
		case "trafficManagerAPI":
			dp = &d.PrivateTrafficManagerAPI
		case "trafficManagerConnect":
			dp = &d.PrivateTrafficManagerConnect
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
func (d *Timeouts) merge(o *Timeouts) {
	if o.PrivateAgentInstall != 0 {
		d.PrivateAgentInstall = o.PrivateAgentInstall
	}
	if o.PrivateApply != 0 {
		d.PrivateApply = o.PrivateApply
	}
	if o.PrivateClusterConnect != 0 {
		d.PrivateClusterConnect = o.PrivateClusterConnect
	}
	if o.PrivateIntercept != 0 {
		d.PrivateIntercept = o.PrivateIntercept
	}
	if o.PrivateProxyDial != 0 {
		d.PrivateProxyDial = o.PrivateProxyDial
	}
	if o.PrivateTrafficManagerAPI != 0 {
		d.PrivateTrafficManagerAPI = o.PrivateTrafficManagerAPI
	}
	if o.PrivateTrafficManagerConnect != 0 {
		d.PrivateTrafficManagerConnect = o.PrivateTrafficManagerConnect
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

var defaultConfig = Config{
	Timeouts: Timeouts{
		PrivateAgentInstall:          120 * time.Second,
		PrivateApply:                 1 * time.Minute,
		PrivateClusterConnect:        20 * time.Second,
		PrivateIntercept:             5 * time.Second,
		PrivateProxyDial:             5 * time.Second,
		PrivateTrafficManagerAPI:     5 * time.Second,
		PrivateTrafficManagerConnect: 60 * time.Second,
	},
	LogLevels: LogLevels{
		UserDaemon: logrus.DebugLevel,
		RootDaemon: logrus.InfoLevel,
	}}

var config *Config

var configOnce = sync.Once{}

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
			dlog.Info(c, "No config found. Using default")
			config = &defaultConfig
		}
	})
	return config
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
