package connector

import (
	"path/filepath"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/pkg/kates"
)

const envPrefix = "TEL_APP_"

func AgentContainer(ata *addTrafficAgentAction, obj kates.Object, appContainer *kates.Container) corev1.Container {
	return corev1.Container{
		Name:  agentContainerName,
		Image: ata.ImageName,
		Args:  []string{"agent"},
		Ports: []corev1.ContainerPort{{
			Name:          ata.ContainerPortName,
			Protocol:      ata.ContainerPortProto,
			ContainerPort: 9900,
		}},
		Env:          ata.agentEnvironment(obj.GetName(), appContainer),
		EnvFrom:      ata.agentEnvFrom(appContainer.EnvFrom),
		VolumeMounts: ata.agentVolumeMounts(appContainer.VolumeMounts),
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

func (ata *addTrafficAgentAction) agentEnvFrom(appEF []corev1.EnvFromSource) []corev1.EnvFromSource {
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

func (ata *addTrafficAgentAction) agentEnvironment(agentName string, appContainer *kates.Container) []corev1.EnvVar {
	appEnv := ata.appEnvironment(appContainer)
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
			Value: strconv.Itoa(int(ata.ContainerPortNumber)),
		})
	if len(appContainer.VolumeMounts) > 0 {
		env = append(env, corev1.EnvVar{
			Name:  "APP_MOUNTS",
			Value: telAppMountPoint,
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
		Value: managerAppName + "." + managerNamespace,
	})
	return env
}

func (ata *addTrafficAgentAction) agentVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	agentMounts := make([]corev1.VolumeMount, len(mounts)+1)
	for i, mount := range mounts {
		mount.MountPath = filepath.Join(telAppMountPoint, mount.MountPath)
		agentMounts[i] = mount
	}
	agentMounts[len(mounts)] = corev1.VolumeMount{
		Name:      agentAnnotationVolumeName,
		MountPath: "/tel_pod_info",
	}
	return agentMounts
}

func (ata *addTrafficAgentAction) appEnvironment(appContainer *kates.Container) []corev1.EnvVar {
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
