package agentconfig

import (
	"fmt"
	"strconv"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
)

func InitContainer(config *Sidecar, agentSecurityContext *core.SecurityContext, coverDir string) *core.Container {
	ic := &core.Container{
		Name:  InitContainerName,
		Image: config.AgentImage,
		Args:  []string{"agent-init"},
		Env: []core.EnvVar{
			{
				Name:  "LOG_LEVEL",
				Value: config.LogLevel.String(),
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
	if agentSecurityContext != nil {
		if uid := agentSecurityContext.RunAsUser; uid != nil {
			ic.Env = append(ic.Env, core.EnvVar{
				Name:  EnvAgentUID,
				Value: strconv.FormatInt(*uid, 10),
			})
		}
		if gid := agentSecurityContext.RunAsGroup; gid != nil {
			ic.Env = append(ic.Env, core.EnvVar{
				Name:  EnvAgentGID,
				Value: strconv.FormatInt(*gid, 10),
			})
		}
	}
	if coverDir != "" {
		ic.Env = append(ic.Env, core.EnvVar{Name: "GOCOVERDIR", Value: coverDir})
		ic.VolumeMounts = append(ic.VolumeMounts, core.VolumeMount{Name: CoverVolumeName, MountPath: coverDir})
	}
	if r := config.InitResources; r != nil {
		ic.Resources = *r
	}
	if s := config.InitSecurityContext; s != nil {
		ic.SecurityContext = s
	}
	return ic
}
