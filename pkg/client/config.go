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
	// AgentInstall is how long to wait for an agent to be installed (i.e. apply of service and deploy manifests)
	AgentInstall time.Duration `json:"agentInstall,omitempty"`
	// Apply is how long to wait for a k8s manifest to be applied
	Apply time.Duration `json:"apply,omitempty"`
	// ClusterConnect is the maximum time to wait for a connection to the cluster to be established
	ClusterConnect time.Duration `json:"clusterConnect,omitempty"`
	// Intercept is the time to wait for an intercept after the agents has been installed
	Intercept time.Duration `json:"intercept,omitempty"`
	// ProxyDial is how long to wait for the proxy to establish an outbound connection
	ProxyDial time.Duration `json:"proxyDial,omitempty"`
	// TrafficManagerConnect is how long to wait for the traffic-manager API to connect
	TrafficManagerAPI time.Duration `json:"trafficManagerAPI,omitempty"`
	// TrafficManagerConnect is how long to wait for the initial port-forwards to the traffic-manager
	TrafficManagerConnect time.Duration `json:"trafficManagerConnect,omitempty"`
}

func CheckTimeout(c context.Context, which *time.Duration, err error) error {
	cErr := c.Err()
	if cErr != context.DeadlineExceeded {
		if cErr != nil {
			return cErr
		}
		return err
	}
	var name, text string
	timeouts := &config.Timeouts
	switch which {
	case &timeouts.AgentInstall:
		name = "agentInstall"
		text = "agent install"
	case &timeouts.Apply:
		name = "apply"
		text = "apply"
	case &timeouts.ClusterConnect:
		name = "clusterConnect"
		text = "cluster connect"
	case &timeouts.Intercept:
		name = "intercept"
		text = "intercept"
	case &timeouts.ProxyDial:
		name = "proxyDial"
		text = "proxy dial"
	case &timeouts.TrafficManagerAPI:
		name = "trafficManagerAPI"
		text = "traffic manager gRPC API"
	case &timeouts.TrafficManagerConnect:
		name = "trafficManagerConnect"
		text = "port-forward connection to the traffic manager"
	default:
		name = "unknown timer"
		text = "unknown timer"
	}
	dir, _ := filelocation.AppUserConfigDir(c)
	return fmt.Errorf("the %s timed out. The current timeout %s can be configured as timeouts.%s in %s",
		text, *which, name, filepath.Join(dir, configFile))
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
			dp = &d.AgentInstall
		case "apply":
			dp = &d.Apply
		case "clusterConnect":
			dp = &d.ClusterConnect
		case "intercept":
			dp = &d.Intercept
		case "proxyDial":
			dp = &d.ProxyDial
		case "trafficManagerAPI":
			dp = &d.TrafficManagerAPI
		case "trafficManagerConnect":
			dp = &d.TrafficManagerConnect
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
	if o.AgentInstall != 0 {
		d.AgentInstall = o.AgentInstall
	}
	if o.Apply != 0 {
		d.Apply = o.Apply
	}
	if o.ClusterConnect != 0 {
		d.ClusterConnect = o.ClusterConnect
	}
	if o.Intercept != 0 {
		d.Intercept = o.Intercept
	}
	if o.ProxyDial != 0 {
		d.ProxyDial = o.ProxyDial
	}
	if o.TrafficManagerAPI != 0 {
		d.TrafficManagerAPI = o.TrafficManagerAPI
	}
	if o.TrafficManagerConnect != 0 {
		d.TrafficManagerConnect = o.TrafficManagerConnect
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
		AgentInstall:          120 * time.Second,
		Apply:                 1 * time.Minute,
		ClusterConnect:        20 * time.Second,
		Intercept:             5 * time.Second,
		ProxyDial:             5 * time.Second,
		TrafficManagerAPI:     5 * time.Second,
		TrafficManagerConnect: 20 * time.Second,
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
			dlog.Error(c, err)
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
