package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

const (
	clientConfigFileName            = "client.yaml"
	agentEnvConfigFileName          = "agent-env.yaml"
	namespaceSelectorConfigFileName = "namespace-selector.yaml"
	cfgConfigMapName                = agentconfig.ManagerAppName
)

type Watcher interface {
	Run(ctx context.Context) error
	GetClientConfigYaml(ctx context.Context) []byte
	GetAgentEnv() AgentEnv
	SelectorChannel() <-chan *labels.Selector

	// ForceEvent is for testing purposes only.
	ForceEvent(ctx context.Context) error
}

type AgentEnv struct {
	Excluded []string `json:"excluded,omitempty"`
}

func (ae AgentEnv) Equal(o AgentEnv) bool {
	return slices.Equal(ae.Excluded, o.Excluded)
}

func rqEqual(r, o *labels.Requirement) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.Key == o.Key && r.Operator == o.Operator && slices.Equal(r.Values, o.Values)
}

type config struct {
	sync.RWMutex
	namespace string

	clientYAML        []byte
	agentEnv          AgentEnv
	namespaceSelector []*labels.Requirement
	selectorChannel   chan *labels.Selector
}

func NewWatcher(namespace string) Watcher {
	return &config{
		namespace:         namespace,
		selectorChannel:   make(chan *labels.Selector, 1),
		namespaceSelector: []*labels.Requirement{nil}, // One nil entry forces the first event on the LabelMatcher channel
	}
}

func (c *config) Run(ctx context.Context) error {
	dlog.Infof(ctx, "Started watcher for ConfigMap %s", cfgConfigMapName)
	defer dlog.Infof(ctx, "Ended watcher for ConfigMap %s", cfgConfigMapName)
	defer close(c.selectorChannel)

	// The WatchConfig will perform an http GET call to the kubernetes API server, and that connection will not remain open forever,
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ctx.Err() == nil {
		w, err := api.ConfigMaps(c.namespace).Watch(ctx, meta.SingleObject(meta.ObjectMeta{Name: cfgConfigMapName}))
		if err != nil {
			return fmt.Errorf("unable to create configmap watcher for %s.%s: %v", cfgConfigMapName, c.namespace, err)
		}
		if !c.configMapEventHandler(ctx, w.ResultChan()) {
			return nil
		}
	}
	return nil
}

func (c *config) ForceEvent(ctx context.Context) error {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	w, err := api.ConfigMaps(c.namespace).Get(ctx, cfgConfigMapName, meta.GetOptions{})
	if err != nil {
		return err
	}
	c.refreshFile(ctx, w.Data)
	return nil
}

// SelectorChannel returns a channel that will emit a selector everytime the label selector configuration changes.
func (c *config) SelectorChannel() <-chan *labels.Selector {
	return c.selectorChannel
}

func (c *config) configMapEventHandler(ctx context.Context, evCh <-chan watch.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case event, ok := <-evCh:
			if !ok {
				return true // restart watcher
			}
			switch event.Type {
			case watch.Deleted:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, nil)
				}
			case watch.Added, watch.Modified:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, m.Data)
				}
			}
		}
	}
}

var AmendClientConfigFunc = AmendClientConfig //nolint:gochecknoglobals // extension point

func AmendClientConfig(ctx context.Context, cfg client.Config) bool {
	nss := namespaces.Get(ctx)
	if !slices.Equal(nss, cfg.Cluster().MappedNamespaces) {
		dlog.Debugf(ctx, "AmendClientConfig: cluster.mappedNamespaces: %v", nss)
		cfg.Cluster().MappedNamespaces = nss
		return true
	}
	return false
}

func (c *config) refreshFile(ctx context.Context, mapData map[string]string) {
	c.Lock()
	defer c.Unlock()
	if yml, ok := mapData[clientConfigFileName]; ok {
		data := []byte(yml)
		if !bytes.Equal(data, c.clientYAML) {
			c.clientYAML = data
			dlog.Debugf(ctx, "Refreshed client config:\n%s", yml)
		}
	} else if len(c.clientYAML) > 0 {
		c.clientYAML = nil
		dlog.Debug(ctx, "Cleared client config")
	}

	ae := AgentEnv{}
	if yml, ok := mapData[agentEnvConfigFileName]; ok {
		data, err := yaml.YAMLToJSON([]byte(yml))
		if err == nil {
			err = json.Unmarshal(data, &ae)
		}
		if err != nil {
			dlog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", agentEnvConfigFileName, err)
		} else {
			sort.Strings(ae.Excluded)
			if !ae.Equal(c.agentEnv) {
				c.agentEnv = ae
				dlog.Debugf(ctx, "Refreshed agent-env:\n%s", yml)
			}
		}
	} else if !c.agentEnv.Equal(ae) {
		c.agentEnv = ae
		dlog.Debug(ctx, "Cleared agent-env")
	}

	if yml, ok := mapData[namespaceSelectorConfigFileName]; ok {
		nsSelector, err := labels.UnmarshalSelector([]byte(yml))
		if err != nil {
			dlog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", namespaceSelectorConfigFileName, err)
		}
		es := nsSelector.GetAllRequirements()
		if !slices.EqualFunc(es, c.namespaceSelector, rqEqual) {
			c.namespaceSelector = es
			dlog.Debugf(ctx, "Refreshed namespaceSelector: %s", yml)
			c.selectorChannel <- &labels.Selector{MatchExpressions: es}
		}
	} else if len(c.namespaceSelector) > 0 {
		c.namespaceSelector = nil
		c.selectorChannel <- nil
		dlog.Debug(ctx, "Cleared namespaceSelector")
	}
}

func (c *config) GetAgentEnv() AgentEnv {
	return c.agentEnv
}

func (c *config) GetClientConfigYaml(ctx context.Context) (ret []byte) {
	c.RLock()
	defer c.RUnlock()
	var cfg client.Config
	if c.clientYAML == nil {
		cfg = client.GetDefaultConfig()
	} else {
		var err error
		cfg, err = client.ParseConfigYAML(ctx, clientConfigFileName, c.clientYAML)
		if err != nil {
			dlog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", clientConfigFileName, err)
			return
		}
	}
	if AmendClientConfigFunc(ctx, cfg) {
		ret, _ = cfg.MarshalYAML()
	} else {
		ret = c.clientYAML
	}
	return ret
}
