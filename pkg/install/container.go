package install

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/v2/pkg/kates"
)

const EnvPrefix = "_TEL_AGENT_"
const InitContainerName = "tel-agent-init"
const AgentUID = int64(7777)

// AgentContainer will return a configured traffic agent
func AgentContainer(
	name string,
	imageName string,
	appContainer *corev1.Container,
	port corev1.ContainerPort,
	appPort int,
	appProto string,
	apiPort int,
	managerNamespace string,
	setGID bool,
) corev1.Container {
	var securityContext *corev1.SecurityContext
	if setGID {
		securityContext = &corev1.SecurityContext{
			RunAsNonRoot: func() *bool { b := true; return &b }(),
			RunAsGroup:   func() *int64 { i := AgentUID; return &i }(),
			RunAsUser:    func() *int64 { i := AgentUID; return &i }(),
		}
	}
	return corev1.Container{
		Name:            AgentContainerName,
		Image:           imageName,
		Args:            []string{"agent"},
		Ports:           []corev1.ContainerPort{port},
		Env:             agentEnvironment(name, appContainer, appPort, appProto, apiPort, managerNamespace, port),
		EnvFrom:         appContainer.EnvFrom,
		VolumeMounts:    agentVolumeMounts(appContainer.VolumeMounts),
		SecurityContext: securityContext,
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

// InitContainer will return a configured init container for an agent.
func InitContainer(imageName string, port corev1.ContainerPort, appPort int) corev1.Container {
	env := []corev1.EnvVar{
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
	return corev1.Container{
		Name:  InitContainerName,
		Image: imageName,
		Args:  []string{"agent-init"},
		Env:   env,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{
					"NET_ADMIN",
				},
			},
		},
	}
}

func agentEnvironment(
	agentName string,
	appContainer *kates.Container,
	appPort int,
	appProto string,
	apiPort int,
	managerNamespace string,
	port corev1.ContainerPort) []corev1.EnvVar {
	appEnv := appEnvironment(appContainer, apiPort)
	env := make([]corev1.EnvVar, len(appEnv), len(appEnv)+7)
	copy(env, appEnv)
	env = append(env,
		corev1.EnvVar{
			Name:  EnvPrefix + "LOG_LEVEL",
			Value: "info",
		},
		corev1.EnvVar{
			Name:  EnvPrefix + "NAME",
			Value: agentName,
		},
		corev1.EnvVar{
			Name: EnvPrefix + "NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		corev1.EnvVar{
			Name: EnvPrefix + "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		corev1.EnvVar{
			Name:  EnvPrefix + "APP_PORT",
			Value: strconv.Itoa(appPort),
		},
		corev1.EnvVar{
			Name:  EnvPrefix + "PORT",
			Value: strconv.Itoa(int(port.ContainerPort)),
		},
	)
	if appProto != "" {
		env = append(env, corev1.EnvVar{
			Name:  EnvPrefix + "APP_PROTO",
			Value: appProto,
		})
	}
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  EnvPrefix + "APP_MOUNTS",
			Value: TelAppMountPoint,
		})

		// Have the agent propagate the mount-points as TELEPRESENCE_MOUNTS to make it easy for the
		// local app to create symlinks.
		mounts := make([]string, len(appContainer.VolumeMounts))
		for i := range appContainer.VolumeMounts {
			mounts[i] = appContainer.VolumeMounts[i].MountPath
		}
		env = append(env, corev1.EnvVar{
			Name:  "TELEPRESENCE_MOUNTS",
			Value: strings.Join(mounts, ":"),
		})
	}
	env = append(env, corev1.EnvVar{
		Name:  EnvPrefix + "MANAGER_HOST",
		Value: ManagerAppName + "." + managerNamespace,
	})
	return env
}

func agentVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	agentMounts := make([]corev1.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		// Keep the ServiceAccount mount unaltered or a new one will be generated
		if !strings.HasPrefix(mount.MountPath, "/var/run/secrets") {
			// Don't use filepath.Join here. The target is never windows
			mount.MountPath = TelAppMountPoint + "/" + strings.TrimPrefix(mount.MountPath, "/")
		}
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = corev1.VolumeMount{
		Name:      AgentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func appEnvironment(appContainer *kates.Container, apiPort int) []corev1.EnvVar {
	appEnv := appContainer.Env
	envCount := len(appEnv)
	envCopy := make([]corev1.EnvVar, envCount, envCount+2)
	copy(envCopy, appEnv)
	if apiPort != 0 {
		envCopy = append(envCopy, corev1.EnvVar{
			Name:  "TELEPRESENCE_API_PORT",
			Value: strconv.Itoa(apiPort),
		})
	}
	envCopy = append(envCopy, corev1.EnvVar{
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
