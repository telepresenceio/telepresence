package install

import (
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
)

const EnvPrefix = "_TEL_AGENT_"
const InitContainerName = "tel-agent-init"

// AgentContainer will return a configured traffic agent
func AgentContainer(
	name string,
	imageName string,
	appContainer *core.Container,
	port core.ContainerPort,
	appPort int,
	appProto string,
	apiPort int,
	managerNamespace string,
) core.Container {
	var securityContext *core.SecurityContext
	return core.Container{
		Name:            AgentContainerName,
		Image:           imageName,
		Args:            []string{"agent"},
		Ports:           []core.ContainerPort{port},
		Env:             agentEnvironment(name, appContainer, appPort, appProto, apiPort, managerNamespace, port),
		EnvFrom:         appContainer.EnvFrom,
		VolumeMounts:    agentVolumeMounts(appContainer.VolumeMounts),
		SecurityContext: securityContext,
		ReadinessProbe: &core.Probe{
			ProbeHandler: core.ProbeHandler{
				Exec: &core.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

// InitContainer will return a configured init container for an agent.
func InitContainer(imageName string, port core.ContainerPort, appPort int) core.Container {
	env := []core.EnvVar{
		{
			Name:  "APP_PORT",
			Value: strconv.Itoa(appPort),
		},
		{
			Name:  "AGENT_PORT",
			Value: strconv.Itoa(int(port.ContainerPort)),
		},
		{
			Name:  "AGENT_PROTOCOL",
			Value: string(port.Protocol),
		},
	}
	return core.Container{
		Name:  InitContainerName,
		Image: imageName,
		Args:  []string{"agent-init"},
		Env:   env,
		SecurityContext: &core.SecurityContext{
			Capabilities: &core.Capabilities{
				Add: []core.Capability{
					"NET_ADMIN",
				},
			},
		},
	}
}

func agentEnvironment(
	agentName string,
	appContainer *core.Container,
	appPort int,
	appProto string,
	apiPort int,
	managerNamespace string,
	port core.ContainerPort) []core.EnvVar {
	appEnv := appEnvironment(appContainer, apiPort)
	env := make([]core.EnvVar, len(appEnv), len(appEnv)+7)
	copy(env, appEnv)
	env = append(env,
		core.EnvVar{
			Name:  EnvPrefix + "LOG_LEVEL",
			Value: "info",
		},
		core.EnvVar{
			Name:  EnvPrefix + "NAME",
			Value: agentName,
		},
		core.EnvVar{
			Name: EnvPrefix + "NAMESPACE",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		core.EnvVar{
			Name: EnvPrefix + "POD_IP",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		core.EnvVar{
			Name:  EnvPrefix + "APP_PORT",
			Value: strconv.Itoa(appPort),
		},
		core.EnvVar{
			Name:  EnvPrefix + "PORT",
			Value: strconv.Itoa(int(port.ContainerPort)),
		},
	)
	if appProto != "" {
		env = append(env, core.EnvVar{
			Name:  EnvPrefix + "APP_PROTO",
			Value: appProto,
		})
	}
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, core.EnvVar{
			Name:  EnvPrefix + "APP_MOUNTS",
			Value: TelAppMountPoint,
		})

		// Have the agent propagate the mount-points as TELEPRESENCE_MOUNTS to make it easy for the
		// local app to create symlinks.
		mounts := make([]string, len(appContainer.VolumeMounts))
		for i := range appContainer.VolumeMounts {
			mounts[i] = appContainer.VolumeMounts[i].MountPath
		}
		env = append(env, core.EnvVar{
			Name:  "TELEPRESENCE_MOUNTS",
			Value: strings.Join(mounts, ":"),
		})
	}
	env = append(env, core.EnvVar{
		Name:  EnvPrefix + "MANAGER_HOST",
		Value: ManagerAppName + "." + managerNamespace,
	})
	return env
}

func agentVolumeMounts(mounts []core.VolumeMount) []core.VolumeMount {
	agentMounts := make([]core.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		// Keep the ServiceAccount mount unaltered or a new one will be generated
		if !strings.HasPrefix(mount.MountPath, "/var/run/secrets") {
			// Don't use filepath.Join here. The target is never windows
			mount.MountPath = TelAppMountPoint + "/" + strings.TrimPrefix(mount.MountPath, "/")
		}
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = core.VolumeMount{
		Name:      AgentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func appEnvironment(appContainer *core.Container, apiPort int) []core.EnvVar {
	appEnv := appContainer.Env
	envCount := len(appEnv)
	envCopy := make([]core.EnvVar, envCount, envCount+2)
	copy(envCopy, appEnv)
	if apiPort != 0 {
		envCopy = append(envCopy, core.EnvVar{
			Name:  "TELEPRESENCE_API_PORT",
			Value: strconv.Itoa(apiPort),
		})
	}
	envCopy = append(envCopy, core.EnvVar{
		Name:  "TELEPRESENCE_CONTAINER",
		Value: appContainer.Name,
	})
	return envCopy
}

const maxPortNameLen = 15

// HiddenPortName prefixes the given name with "tm-" and truncates it to 15 characters. If
// the ordinal is greater than zero, the last two digits are reserved for the hexadecimal
// representation of that ordinal.
func HiddenPortName(name string, ordinal int) string {
	// New name must be max 15 characters long
	hiddenName := "tm-" + name
	if len(hiddenName) > maxPortNameLen {
		if ordinal > 0 {
			hiddenName = hiddenName[:maxPortNameLen-2] + strconv.FormatInt(int64(ordinal), 16) // we don't expect more than 256 ports
		} else {
			hiddenName = hiddenName[:maxPortNameLen]
		}
	}
	return hiddenName
}
