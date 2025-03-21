package agentconfig

import (
	"fmt"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
)

func InitContainer(config *Sidecar) *core.Container {
	ic := &core.Container{
		Name:  InitContainerName,
		Image: config.AgentImage,
		Args:  []string{"agent-init"},
		Env: []core.EnvVar{
			{
				Name:  "LOG_LEVEL",
				Value: config.LogLevel,
			},
			{
				Name: "AGENT_CONFIG",
				ValueFrom: &core.EnvVarSource{
					FieldRef: &core.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  fmt.Sprintf("metadata.annotations['%s']", annotation.Config),
					},
				},
			},
			{
				Name: "POD_IP",
				ValueFrom: &core.EnvVarSource{
					FieldRef: &core.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "status.podIP",
					},
				},
			},
		},
		SecurityContext: &core.SecurityContext{
			Capabilities: &core.Capabilities{
				Add: []core.Capability{"NET_ADMIN"},
			},
		},
	}
	if r := config.InitResources; r != nil {
		ic.Resources = *r
	}
	if s := config.InitSecurityContext; s != nil {
		ic.SecurityContext = s
	}
	return ic
}
