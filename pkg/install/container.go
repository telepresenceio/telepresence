package install

import (
	"path/filepath"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/pkg/kates"
)

const envPrefix = "_TEL_AGENT_"

func AgentContainer(
	name string,
	imageName string,
	appContainer *corev1.Container,
	port corev1.ContainerPort,
	appPort int,
	managerNamespace string,
) corev1.Container {
	return corev1.Container{
		Name:         AgentContainerName,
		Image:        imageName,
		Args:         []string{"agent"},
		Ports:        []corev1.ContainerPort{port},
		Env:          agentEnvironment(name, appContainer, appPort, managerNamespace),
		EnvFrom:      appContainer.EnvFrom,
		VolumeMounts: agentVolumeMounts(appContainer.VolumeMounts),
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

func agentEnvironment(agentName string, appContainer *kates.Container, appPort int, managerNamespace string) []corev1.EnvVar {
	appEnv := appEnvironment(appContainer)
	env := make([]corev1.EnvVar, len(appEnv), len(appEnv)+7)
	copy(env, appEnv)
	env = append(env,
		corev1.EnvVar{
			Name:  envPrefix + "LOG_LEVEL",
			Value: "debug",
		},
		corev1.EnvVar{
			Name:  envPrefix + "NAME",
			Value: agentName,
		},
		corev1.EnvVar{
			Name: envPrefix + "NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		corev1.EnvVar{
			Name: envPrefix + "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		corev1.EnvVar{
			Name:  envPrefix + "APP_PORT",
			Value: strconv.Itoa(appPort),
		})
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  envPrefix + "APP_MOUNTS",
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
		Name:  envPrefix + "MANAGER_HOST",
		Value: ManagerAppName + "." + managerNamespace,
	})
	return env
}

func agentVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	agentMounts := make([]corev1.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		// Keep the ServiceAccount mount unaltered or a new one will be generated
		if !strings.HasPrefix(mount.MountPath, "/var/run/secrets") {
			mount.MountPath = filepath.Join(TelAppMountPoint, mount.MountPath)
		}
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = corev1.VolumeMount{
		Name:      AgentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func appEnvironment(appContainer *kates.Container) []corev1.EnvVar {
	appEnv := appContainer.Env
	envCount := len(appEnv)
	envCopy := make([]corev1.EnvVar, envCount+1)
	copy(envCopy, appEnv)
	envCopy[envCount] = corev1.EnvVar{
		Name:  "TELEPRESENCE_CONTAINER",
		Value: appContainer.Name,
	}
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
