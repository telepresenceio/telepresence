package install

import (
	"path/filepath"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/pkg/kates"
)

const envPrefix = "TEL_APP_"

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
		EnvFrom:      agentEnvFrom(appContainer.EnvFrom),
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

func agentEnvFrom(appEF []corev1.EnvFromSource) []corev1.EnvFromSource {
	if ln := len(appEF); ln > 0 {
		agentEF := make([]corev1.EnvFromSource, ln)
		for i, appE := range appEF {
			appE.Prefix = envPrefix + appE.Prefix
			agentEF[i] = appE
		}
		return agentEF
	}
	return appEF
}

func agentEnvironment(agentName string, appContainer *kates.Container, appPort int, managerNamespace string) []corev1.EnvVar {
	appEnv := appEnvironment(appContainer)
	env := make([]corev1.EnvVar, len(appEnv), len(appEnv)+7)
	copy(env, appEnv)
	env = append(env,
		corev1.EnvVar{
			Name:  "LOG_LEVEL",
			Value: "debug",
		},
		corev1.EnvVar{
			Name:  "AGENT_NAME",
			Value: agentName,
		},
		corev1.EnvVar{
			Name: "AGENT_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		corev1.EnvVar{
			Name: "AGENT_POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		corev1.EnvVar{
			Name:  "APP_PORT",
			Value: strconv.Itoa(appPort),
		})
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  "APP_MOUNTS",
			Value: TelAppMountPoint,
		})

		// Have the agent propagate the mount-points as TELEPRESENCE_MOUNTS to make it easy for the
		// local app to create symlinks.
		mounts := make([]string, len(appContainer.VolumeMounts))
		for i := range appContainer.VolumeMounts {
			mounts[i] = appContainer.VolumeMounts[i].MountPath
		}
		env = append(env, corev1.EnvVar{
			Name:  envPrefix + "TELEPRESENCE_MOUNTS",
			Value: strings.Join(mounts, ":"),
		})
	}
	env = append(env, corev1.EnvVar{
		Name:  "MANAGER_HOST",
		Value: ManagerAppName + "." + managerNamespace,
	})
	return env
}

func agentVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	agentMounts := make([]corev1.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		mount.MountPath = filepath.Join(TelAppMountPoint, mount.MountPath)
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = corev1.VolumeMount{
		Name:      AgentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func appEnvironment(appContainer *kates.Container) []corev1.EnvVar {
	envCopy := make([]corev1.EnvVar, len(appContainer.Env)+1)
	for i, ev := range appContainer.Env {
		ev.Name = envPrefix + ev.Name
		envCopy[i] = ev
	}
	envCopy[len(appContainer.Env)] = corev1.EnvVar{
		Name:  "TELEPRESENCE_CONTAINER",
		Value: appContainer.Name,
	}
	return envCopy
}
