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

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
)

type Watcher interface {
	Run(ctx context.Context) error
	GetClientConfigYaml(ctx context.Context) []byte
	GetAgentStateYaml(ctx context.Context) []byte
	SetAgentStateYaml(ctx context.Context, newAgentStateYAML []byte)
	GetAgentEnv() AgentEnv
	SelectorChannel() <-chan *labels.Selector
	AdminCommandChannel() <-chan tmconfig.AdminCommandList

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
	agentStateYAML    []byte
	adminCommandsYAML []byte
	agentEnv          AgentEnv
	namespaceSelector []*labels.Requirement
	selectorChannel   chan *labels.Selector
	commandsChannel   chan tmconfig.AdminCommandList
}

func NewWatcher(namespace string) Watcher {
	return &config{
		namespace:         namespace,
		selectorChannel:   make(chan *labels.Selector, 1),
		commandsChannel:   make(chan tmconfig.AdminCommandList, 1),
		namespaceSelector: []*labels.Requirement{nil}, // One nil entry forces the first event on the LabelMatcher channel
	}
}

func (c *config) Run(ctx context.Context) error {
	clog.Infof(ctx, "Started watcher for ConfigMap %s", tmconfig.CfgConfigMapName)
	defer clog.Infof(ctx, "Ended watcher for ConfigMap %s", tmconfig.CfgConfigMapName)
	defer close(c.selectorChannel)

	// The WatchConfig will perform an http GET call to the kubernetes API server, and that connection will not remain open forever,
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ctx.Err() == nil {
		w, err := api.ConfigMaps(c.namespace).Watch(ctx, meta.SingleObject(meta.ObjectMeta{Name: tmconfig.CfgConfigMapName}))
		if err != nil {
			return fmt.Errorf("unable to create configmap watcher for %s.%s: %v", tmconfig.CfgConfigMapName, c.namespace, err)
		}
		if !c.configMapEventHandler(ctx, w.ResultChan()) {
			return nil
		}
	}
	return nil
}

func (c *config) ForceEvent(ctx context.Context) error {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	w, err := api.ConfigMaps(c.namespace).Get(ctx, tmconfig.CfgConfigMapName, meta.GetOptions{})
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

// AdminCommandChannel returns a channel that will emit a [tmconfig.AdminCommandList] list whenever the
// admin commands configuration (from admin-commands.yaml in the manager ConfigMap) is reloaded.
func (c *config) AdminCommandChannel() <-chan tmconfig.AdminCommandList {
	return c.commandsChannel
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
					clog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, nil)
				}
			case watch.Added, watch.Modified:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					clog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, m.Data)
				}
			}
		}
	}
}

func AmendClientConfig(ctx context.Context, cfg client.Config) bool {
	nss := namespaces.Get(ctx)
	changed := false
	if !slices.Equal(nss, cfg.Cluster().MappedNamespaces) {
		clog.Debugf(ctx, "AmendClientConfig: cluster.mappedNamespaces: %v", nss)
		cfg.Cluster().MappedNamespaces = nss
		changed = true
	}
	// The intercept timeout must be equal to or greater than the agent arrival timeout.
	env := managerutil.GetEnv(ctx)
	if env.AgentArrivalTimeout > cfg.Timeouts().PrivateIntercept {
		clog.Debugf(ctx, "AmendClientConfig: timeouts.privateIntercept: %v", env.AgentArrivalTimeout)
		cfg.Timeouts().PrivateIntercept = env.AgentArrivalTimeout
		changed = true
	}
	return changed
}

func (c *config) refreshFile(ctx context.Context, mapData map[string]string) {
	c.Lock()
	defer c.Unlock()
	if yml, ok := mapData[tmconfig.ClientConfigFileName]; ok {
		data := []byte(yml)
		if !bytes.Equal(data, c.clientYAML) {
			c.clientYAML = data
		}
	} else if len(c.clientYAML) > 0 {
		c.clientYAML = nil
		clog.Debug(ctx, "Cleared client config")
	}

	ae := AgentEnv{}
	if yml, ok := mapData[tmconfig.AgentEnvConfigFileName]; ok {
		data, err := yaml.YAMLToJSON([]byte(yml))
		if err == nil {
			err = json.Unmarshal(data, &ae)
		}
		if err != nil {
			clog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", tmconfig.AgentEnvConfigFileName, err)
		} else {
			sort.Strings(ae.Excluded)
			if !ae.Equal(c.agentEnv) {
				c.agentEnv = ae
				clog.Debugf(ctx, "Refreshed agent-env:\n%s", yml)
			}
		}
	} else if !c.agentEnv.Equal(ae) {
		c.agentEnv = ae
		clog.Debug(ctx, "Cleared agent-env")
	}

	if yml, ok := mapData[tmconfig.NamespaceSelectorConfigFileName]; ok {
		nsSelector, err := labels.UnmarshalSelector([]byte(yml))
		if err != nil {
			clog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", tmconfig.NamespaceSelectorConfigFileName, err)
		} else {
			es := nsSelector.GetAllRequirements()
			if !slices.EqualFunc(es, c.namespaceSelector, rqEqual) {
				select {
				case c.selectorChannel <- &labels.Selector{MatchExpressions: es}:
					c.namespaceSelector = es
					clog.Debugf(ctx, "Refreshed namespaceSelector: %s", yml)
				default:
					clog.Warnf(ctx, "Unable to refreshed namespaceSelector: %s", yml)
				}
			}
		}
	} else if len(c.namespaceSelector) > 0 {
		select {
		case c.selectorChannel <- nil:
			c.namespaceSelector = nil
			clog.Debug(ctx, "Cleared namespaceSelector")
		default:
			clog.Warnf(ctx, "Unable to clear namespaceSelector")
		}
	}
	if yml, ok := mapData[tmconfig.AgentStateFileName]; ok {
		data := []byte(yml)
		if !bytes.Equal(data, c.agentStateYAML) {
			c.agentStateYAML = data
			clog.Debugf(ctx, "Refreshed agent state:\n%s", yml)
		}
	} else if len(c.agentStateYAML) > 0 {
		c.agentStateYAML = nil
		clog.Debug(ctx, "Cleared agent state")
	}
	if yml, ok := mapData[tmconfig.AdminCommandsFileName]; ok {
		data := []byte(yml)
		if !bytes.Equal(data, c.adminCommandsYAML) {
			var al tmconfig.AdminCommandList
			err := yaml.Unmarshal(data, &al)
			if err != nil {
				clog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", tmconfig.AdminCommandsFileName, err)
			} else {
				select {
				case c.commandsChannel <- al:
					c.adminCommandsYAML = data
					clog.Debugf(ctx, "Refreshed admin commands:\n%s", yml)
				default:
					clog.Warnf(ctx, "Unable to refreshed admin commands:\n%s", yml)
				}
			}
		}
	} else if len(c.adminCommandsYAML) > 0 {
		c.adminCommandsYAML = nil
		clog.Debug(ctx, "Cleared admin commands")
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
		cfg, err = client.ParseConfigYAML(ctx, tmconfig.ClientConfigFileName, c.clientYAML)
		if err != nil {
			clog.Errorf(ctx, "failed to unmarshal YAML from %s: %v", tmconfig.ClientConfigFileName, err)
			return ret
		}
	}
	if AmendClientConfig(ctx, cfg) {
		ret, _ = cfg.MarshalYAML()
	} else {
		ret = c.clientYAML
	}
	clog.Debugf(ctx, "Client config:\n%s", string(ret))
	return ret
}

func (c *config) GetAgentStateYaml(ctx context.Context) (ret []byte) {
	return c.agentStateYAML
}

func (c *config) GetAdminCommandsYaml(ctx context.Context) (ret []byte) {
	return c.adminCommandsYAML
}

func (c *config) SetAgentStateYaml(ctx context.Context, newAgentStateYAML []byte) {
	c.agentStateYAML = newAgentStateYAML
}
