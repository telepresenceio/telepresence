package agentconfig

import (
	"fmt"
	"strings"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func AgentVolumes(agentName string, pod *core.Pod) (volumes []core.Volume, err error) {
	volumes = []core.Volume{
		{
			Name: PodInfoVolumeName,
			VolumeSource: core.VolumeSource{
				DownwardAPI: &core.DownwardAPIVolumeSource{
					Items: []core.DownwardAPIVolumeFile{
						{
							Path: "annotations",
							FieldRef: &core.ObjectFieldSelector{
								FieldPath: "metadata.annotations",
							},
						},
					},
					DefaultMode: nil,
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

	if pod == nil {
		// Called from genyaml so no pod is available to provide annotations.
		return volumes, nil
	}

	// The name of the TLS secret in the annotations might contain environment variable expansions. The expansions
	// allowed here are "$AGENT_NAME" and "$_TEL_AGENT_NAME". The latter is for backward compatibility with older
	// agents where this expansion happened in the traffic-agent.
	env := dos.MapEnv{"AGENT_NAME": agentName}
	volumes, err = appendSecretVolume(env, annotation.DownstreamTLSSecret, pod, volumes)
	if err != nil {
		return nil, err
	}
	return appendSecretVolume(env, annotation.UpstreamTLSSecret, pod, volumes)
}

func appendSecretVolume(env dos.Env, annotation string, pod *core.Pod, volumes []core.Volume) ([]core.Volume, error) {
	secretsAndPorts, err := annotationPrefixedPorts(pod.Annotations, annotation)
	if err != nil || len(secretsAndPorts) == 0 {
		return volumes, err
	}
	for _, secret := range maps.SortedKeys(secretsAndPorts) {
		volumes = append(volumes, core.Volume{
			Name: fmt.Sprintf("%s-vol", secret),
			VolumeSource: core.VolumeSource{
				Secret: &core.SecretVolumeSource{
					SecretName: env.ExpandEnv(secret),
				},
			},
		})
	}
	return volumes, nil
}

func (a *ContainerBuilder) appendVolumeMounts(app *core.Container, cc *Container, mounts []core.VolumeMount) []core.VolumeMount {
	pfx := EnvPrefixApp + cc.EnvPrefix
	for _, m := range app.VolumeMounts {
		mp := a.Config.MountPolicies.Get(m.Name, m.MountPath)
		switch mp {
		case types.MountPolicyIgnore, types.MountPolicyLocal:
		case types.MountPolicyRemoteReadOnly:
			if !m.ReadOnly {
				rco := core.RecursiveReadOnlyIfPossible
				m.ReadOnly = true
				m.RecursiveReadOnly = &rco
			}
			fallthrough
		default:
			m.Name = prefixInterpolated(m.Name, pfx)
			m.MountPath = prefixInterpolated(cc.MountPoint+"/"+strings.TrimPrefix(m.MountPath, "/"), pfx)
			m.SubPath = prefixInterpolated(m.SubPath, pfx)
			m.SubPathExpr = prefixInterpolated(m.SubPathExpr, pfx)
			mounts = append(mounts, m)
		}
	}
	return mounts
}
