package tmconfig

import (
	"context"
	"slices"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	ClientConfigFileName            = "client.yaml"
	AgentEnvConfigFileName          = "agent-env.yaml"
	AdminCommandsFileName           = "admin-commands.yaml"
	NamespaceSelectorConfigFileName = "namespace-selector.yaml"
	AgentStateFileName              = "agent-state.yaml"
	CfgConfigMapName                = agentconfig.ManagerAppName
)

func ReadConfig(ctx context.Context, namespace string) (*core.ConfigMap, error) {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	return api.ConfigMaps(namespace).Get(ctx, CfgConfigMapName, meta.GetOptions{})
}

func UpdateConfig(ctx context.Context, cm *core.ConfigMap) (*core.ConfigMap, error) {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	return api.ConfigMaps(cm.Namespace).Update(ctx, cm, meta.UpdateOptions{})
}

// AddCommand adds a command to the configmap.
func AddCommand(ctx context.Context, namespace string, command AdminCommand) error {
	cm, err := ReadConfig(ctx, namespace)
	if err != nil {
		return err
	}
	data := []byte(cm.Data[AdminCommandsFileName])
	var commands AdminCommandList
	if len(data) > 0 {
		err = commands.UnmarshalYAML(data)
		if err != nil {
			return err
		}
	}
	commands = append(commands, command)
	data, err = commands.MarshalYAML()
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[AdminCommandsFileName] = string(data)
	_, err = UpdateConfig(ctx, cm)
	return err
}

// ClearCommands removes all commands older than, or equal to, the given time.
func ClearCommands(ctx context.Context, namespace string, olderThan int64) error {
	cm, err := ReadConfig(ctx, namespace)
	if err != nil {
		return err
	}
	data := []byte(cm.Data[AdminCommandsFileName])
	if len(data) == 0 {
		return nil
	}
	var commands AdminCommandList
	err = commands.UnmarshalYAML(data)
	if err != nil {
		return err
	}
	szBefore := len(commands)
	commands = slices.DeleteFunc(commands, func(command AdminCommand) bool { return command.Timestamp <= olderThan })
	if len(commands) == szBefore {
		return nil
	}
	data, err = commands.MarshalYAML()
	if err != nil {
		return err
	}
	cm.Data[AdminCommandsFileName] = string(data)
	_, err = UpdateConfig(ctx, cm)
	return err
}
