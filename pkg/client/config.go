package client

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const configFile = "config.yml"

type Config struct {
	Timeouts Timeouts `json:"timeouts,omitempty"`
}

// merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *Config) merge(o *Config) {
	c.Timeouts.merge(&o.Timeouts)
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
	case &timeouts.TrafficManagerConnect:
		name = "trafficManagerConnect"
		text = "traffic manager connect"
	default:
		name = "unknown timer"
		text = "unknown timer"
	}
	dir, _ := filelocation.AppUserConfigDir(c)
	return fmt.Errorf("the %s timed out. The current timeout %s can be configured as timeout.%s in %s",
		text, *which, name, filepath.Join(dir, configFile))
}

// UnmarshalYAML caters for the unfortunate fact that time.Duration doesn't do YAML or JSON at all.
func (d *Timeouts) UnmarshalYAML(unmarshal func(interface{}) error) (err error) {
	var fields map[string]interface{}
	if err = unmarshal(&fields); err != nil {
		return err
	}
	get := func(key string) (value time.Duration, err error) {
		if jv, ok := fields[key]; ok {
			switch jv := jv.(type) {
			case string:
				if value, err = time.ParseDuration(jv); err != nil {
					err = fmt.Errorf("timeouts.%s: %q is not a valid duration", key, jv)
				}
			case float64:
				value = time.Duration(jv * float64(time.Second))
			case int:
				value = time.Duration(jv) * time.Second
			default:
				err = fmt.Errorf("timeouts.%s: %v is not a valid duration", key, jv)
			}
		}
		return value, err
	}
	if d.AgentInstall, err = get("agentInstall"); err != nil {
		return err
	}
	if d.Apply, err = get("apply"); err != nil {
		return err
	}
	if d.ClusterConnect, err = get("clusterConnect"); err != nil {
		return err
	}
	if d.Intercept, err = get("intercept"); err != nil {
		return err
	}
	if d.ProxyDial, err = get("proxyDial"); err != nil {
		return err
	}
	d.TrafficManagerConnect, err = get("trafficManagerConnect")
	return err
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
	if o.TrafficManagerConnect != 0 {
		d.TrafficManagerConnect = o.TrafficManagerConnect
	}
}

var defaultConfig = Config{Timeouts: Timeouts{
	ClusterConnect:        20 * time.Second,
	TrafficManagerConnect: 20 * time.Second,
	Apply:                 1 * time.Minute,
	Intercept:             5 * time.Second,
	AgentInstall:          120 * time.Second,
	ProxyDial:             5 * time.Second,
}}

var config *Config

var configOnce = sync.Once{}

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

func loadConfig(c context.Context) (*Config, error) {
	dirs, err := filelocation.AppSystemConfigDirs(c)
	if err != nil {
		return nil, err
	}

	cfg := defaultConfig // start with a by value copy of the default

	readMerge := func(dir string) error {
		bs, err := ioutil.ReadFile(filepath.Join(dir, configFile))
		if err != nil {
			if err == os.ErrNotExist {
				err = nil
			}
			return err
		}
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
