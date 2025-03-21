package agentconfig

import (
	"strings"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func AgentVolumes() []core.Volume {
	volumes := []core.Volume{
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
	return volumes
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
