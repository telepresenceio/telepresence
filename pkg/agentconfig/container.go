package agentconfig

import (
	"strings"

	v1 "k8s.io/api/core/v1"
)

// AgentContainer will return a configured traffic-agent
func AgentContainer(
	pod *v1.Pod,
	config *Sidecar,
) *v1.Container {
	ports := make([]v1.ContainerPort, 0, 5)
	for _, cc := range config.Containers {
		for _, ic := range cc.Intercepts {
			ports = append(ports, v1.ContainerPort{
				Name:          ic.ContainerPortName,
				ContainerPort: int32(ic.AgentPort),
				Protocol:      v1.Protocol(ic.Protocol),
			})
		}
	}
	if len(ports) == 0 {
		return nil
	}

	evs := make([]v1.EnvVar, 0, len(config.Containers)*5)
	efs := make([]v1.EnvFromSource, 0, len(config.Containers)*3)
	EachContainer(pod, config, func(app *v1.Container, cc *Container) {
		evs = appendAppContainerEnv(app, cc, evs)
		efs = appendAppContainerEnvFrom(app, cc, efs)
	})
	evs = append(evs,
		v1.EnvVar{
			Name: EnvPrefixAgent + "POD_IP",
			ValueFrom: &v1.EnvVarSource{
				FieldRef: &v1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		})

	mounts := make([]v1.VolumeMount, 0, len(config.Containers)*3)
	EachContainer(pod, config, func(app *v1.Container, cc *Container) {
		mounts = appendAppContainerVolumeMounts(app, cc, mounts)
	})

	mounts = append(mounts,
		v1.VolumeMount{
			Name:      AnnotationVolumeName,
			MountPath: AnnotationMountPoint,
		},
		v1.VolumeMount{
			Name:      ConfigVolumeName,
			MountPath: ConfigMountPoint,
		},
		v1.VolumeMount{
			Name:      ExportsVolumeName,
			MountPath: ExportsMountPoint,
		},
	)

	if len(efs) == 0 {
		efs = nil
	}
	return &v1.Container{
		Name:         ContainerName,
		Image:        config.AgentImage,
		Args:         []string{"agent"},
		Ports:        ports,
		Env:          evs,
		EnvFrom:      efs,
		VolumeMounts: mounts,
		ReadinessProbe: &v1.Probe{
			ProbeHandler: v1.ProbeHandler{
				Exec: &v1.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
}

// EachContainer will find each container in the given config and match it against a container
// in the pod using its name. The given function is called once for each match.
func EachContainer(pod *v1.Pod, config *Sidecar, f func(*v1.Container, *Container)) {
	cns := pod.Spec.Containers
	for _, cc := range config.Containers {
		for i := range pod.Spec.Containers {
			if app := &cns[i]; app.Name == cc.Name {
				f(app, cc)
				break
			}
		}
	}
}

func appendAppContainerVolumeMounts(app *v1.Container, cc *Container, mounts []v1.VolumeMount) []v1.VolumeMount {
	for _, m := range app.VolumeMounts {
		if !strings.HasPrefix(m.MountPath, "/var/run/secrets/") {
			m.MountPath = cc.MountPoint + "/" + strings.TrimPrefix(m.MountPath, "/")
		}
		mounts = append(mounts, m)
	}
	return mounts
}

func appendAppContainerEnv(app *v1.Container, cc *Container, es []v1.EnvVar) []v1.EnvVar {
	for _, e := range app.Env {
		e.Name = EnvPrefixApp + cc.EnvPrefix + e.Name
		es = append(es, e)
	}
	return es
}

func appendAppContainerEnvFrom(app *v1.Container, cc *Container, es []v1.EnvFromSource) []v1.EnvFromSource {
	for _, e := range app.EnvFrom {
		e.Prefix = EnvPrefixApp + cc.EnvPrefix + e.Prefix
		es = append(es, e)
	}
	return es
}
