package agentconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
)

// AgentContainer will return a configured traffic-agent.
func AgentContainer(
	ctx context.Context,
	pod *core.Pod,
	config *Sidecar,
) *core.Container {
	ports := make([]core.ContainerPort, 0, 5)
	for _, cc := range config.Containers {
		for _, ic := range PortUniqueIntercepts(cc) {
			ports = append(ports, core.ContainerPort{
				Name:          ic.ContainerPortName,
				ContainerPort: int32(ic.AgentPort),
				Protocol:      ic.Protocol,
			})
		}
	}
	if len(ports) == 0 {
		return nil
	}

	evs := make([]core.EnvVar, 0, len(config.Containers)*5)
	efs := make([]core.EnvFromSource, 0, len(config.Containers)*3)
	subst := make(map[string]string, len(evs)+len(efs))
	EachContainer(pod, config, func(app *core.Container, cc *Container) {
		evs = appendAppContainerEnv(app, cc, evs, subst)
		efs = appendAppContainerEnvFrom(app, cc, efs, subst)
	})
	if config.APIPort > 0 {
		evs = append(evs, core.EnvVar{
			Name:  EnvAPIPort,
			Value: strconv.Itoa(int(config.APIPort)),
		})
	}
	evs = append(evs,
		core.EnvVar{
			Name: EnvPrefixAgent + "POD_IP",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		},
		core.EnvVar{
			Name: EnvPrefixAgent + "NAME",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				},
			},
		})

	mounts := make([]core.VolumeMount, 0, len(config.Containers)*3)
	EachContainer(pod, config, func(app *core.Container, cc *Container) {
		mounts = appendAppContainerVolumeMounts(app, cc, mounts, pod.ObjectMeta.Annotations)
	})

	mounts = append(mounts,
		core.VolumeMount{
			Name:      AnnotationVolumeName,
			MountPath: AnnotationMountPoint,
		},
		core.VolumeMount{
			Name:      ConfigVolumeName,
			MountPath: ConfigMountPoint,
		},
		core.VolumeMount{
			Name:      ExportsVolumeName,
			MountPath: ExportsMountPoint,
		},
		core.VolumeMount{
			Name:      TempVolumeName,
			MountPath: TempMountPoint,
		},
	)
	if _, ok := pod.ObjectMeta.Annotations[TerminatingTLSSecretAnnotation]; ok {
		mounts = append(mounts, core.VolumeMount{
			Name:      TerminatingTLSVolumeName,
			MountPath: TerminatingTLSMountPoint,
		})
	}

	if _, ok := pod.ObjectMeta.Annotations[OriginatingTLSSecretAnnotation]; ok {
		mounts = append(mounts, core.VolumeMount{
			Name:      OriginatingTLSVolumeName,
			MountPath: OriginatingTLSMountPoint,
		})
	}

	if len(efs) == 0 {
		efs = nil
	}

	ac := &core.Container{
		Name:         ContainerName,
		Image:        config.AgentImage,
		Args:         []string{"agent"},
		Ports:        ports,
		Env:          evs,
		EnvFrom:      efs,
		VolumeMounts: mounts,
		ReadinessProbe: &core.Probe{
			ProbeHandler: core.ProbeHandler{
				Exec: &core.ExecAction{
					Command: []string{"/bin/stat", "/tmp/agent/ready"},
				},
			},
		},
	}
	if r := config.Resources; r != nil {
		ac.Resources = *r
	}

	// Replace all occurrences of "$(ENV" with "$(PFX_ENV"
	aj, err := json.Marshal(&ac)
	if err != nil {
		dlog.Error(ctx, err)
		return nil
	}
	for k, pk := range subst {
		aj = bytes.ReplaceAll(aj, []byte("$("+k), []byte("$("+pk))
	}
	if err = json.Unmarshal(aj, &ac); err != nil {
		dlog.Error(ctx, err)
		return nil
	}
	return ac
}

func InitContainer(config *Sidecar) *core.Container {
	ic := &core.Container{
		Name:  InitContainerName,
		Image: config.AgentImage,
		Args:  []string{"agent-init"},
		VolumeMounts: []core.VolumeMount{{
			Name:      ConfigVolumeName,
			MountPath: ConfigMountPoint,
		}},
		SecurityContext: &core.SecurityContext{
			Capabilities: &core.Capabilities{
				Add: []core.Capability{"NET_ADMIN"},
			},
		},
	}
	if r := config.InitResources; r != nil {
		ic.Resources = *r
	}
	return ic
}

func AgentVolumes(agentName string, pod *core.Pod) []core.Volume {
	var items []core.KeyToPath
	if agentName != "" {
		items = []core.KeyToPath{{
			Key:  agentName,
			Path: ConfigFile,
		}}
	}
	volumes := []core.Volume{
		{
			Name: AnnotationVolumeName,
			VolumeSource: core.VolumeSource{
				DownwardAPI: &core.DownwardAPIVolumeSource{
					Items: []core.DownwardAPIVolumeFile{
						{
							FieldRef: &core.ObjectFieldSelector{
								APIVersion: "v1",
								FieldPath:  "metadata.annotations",
							},
							Path: "annotations",
						},
					},
				},
			},
		},
		{
			Name: ConfigVolumeName,
			VolumeSource: core.VolumeSource{
				ConfigMap: &core.ConfigMapVolumeSource{
					LocalObjectReference: core.LocalObjectReference{Name: ConfigMap},
					Items:                items,
				},
			},
		},
		{
			Name: ExportsVolumeName,
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
		{
			Name: TempVolumeName,
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
	}
	if secret, ok := pod.ObjectMeta.Annotations[TerminatingTLSSecretAnnotation]; ok {
		volumes = append(volumes, core.Volume{
			Name: TerminatingTLSVolumeName,
			VolumeSource: core.VolumeSource{
				Secret: &core.SecretVolumeSource{
					SecretName: secret,
				},
			},
		})
	}
	if secret, ok := pod.ObjectMeta.Annotations[OriginatingTLSSecretAnnotation]; ok {
		volumes = append(volumes, core.Volume{
			Name: OriginatingTLSVolumeName,
			VolumeSource: core.VolumeSource{
				Secret: &core.SecretVolumeSource{
					SecretName: secret,
				},
			},
		})
	}
	return volumes
}

// EachContainer will find each container in the given config and match it against a container
// in the pod using its name. The given function is called once for each match.
func EachContainer(pod *core.Pod, config *Sidecar, f func(*core.Container, *Container)) {
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

func appendAppContainerVolumeMounts(app *core.Container, cc *Container, mounts []core.VolumeMount, annotations map[string]string) []core.VolumeMount {
	isVrs := func(s string) bool {
		return strings.HasPrefix(s, "/var/run/secrets/")
	}

	// Does the current mounts slice contain the vrs?
	vrsAdded := func() bool {
		for _, m := range mounts {
			if isVrs(m.MountPath) {
				return true
			}
		}
		return false
	}

	ignoredVolumeMounts := getIgnoredVolumeMounts(annotations)

	for _, m := range app.VolumeMounts {
		if _, ok := ignoredVolumeMounts[m.Name]; ok {
			continue
		}
		if isVrs(m.MountPath) {
			if vrsAdded() {
				continue // Only add /var/run/secrets once, not once per container
			}
		} else {
			m.MountPath = cc.MountPoint + "/" + strings.TrimPrefix(m.MountPath, "/")
		}

		mounts = append(mounts, m)
	}
	return mounts
}

func appendAppContainerEnv(app *core.Container, cc *Container, es []core.EnvVar, subst map[string]string) []core.EnvVar {
	for _, e := range app.Env {
		pn := EnvPrefixApp + cc.EnvPrefix + e.Name
		subst[e.Name] = pn
		e.Name = pn
		es = append(es, e)
	}
	return es
}

func appendAppContainerEnvFrom(app *core.Container, cc *Container, es []core.EnvFromSource, subst map[string]string) []core.EnvFromSource {
	for _, e := range app.EnvFrom {
		pn := EnvPrefixApp + cc.EnvPrefix + e.Prefix
		subst[e.Prefix] = pn
		e.Prefix = pn
		es = append(es, e)
	}
	return es
}

func getIgnoredVolumeMounts(annotations map[string]string) map[string]struct{} {
	vmSlice := strings.Split(annotations["telepresence.getambassador.io/inject-ignore-volume-mounts"], ",")
	ivms := make(map[string]struct{}, len(vmSlice))
	for _, vm := range vmSlice {
		ivms[strings.TrimSpace(vm)] = struct{}{}
	}
	return ivms
}
