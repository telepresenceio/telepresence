package mount

import (
	"context"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Info struct {
	LocalDir  string              `json:"local_dir,omitempty"     yaml:"local_dir,omitempty"`
	RemoteDir string              `json:"remote_dir,omitempty"    yaml:"remote_dir,omitempty"`
	Error     string              `json:"error,omitempty"         yaml:"error,omitempty"`
	PodIP     string              `json:"pod_ip,omitempty"        yaml:"pod_ip,omitempty"`
	Port      uint16              `json:"port,omitempty"          yaml:"port,omitempty"`
	Mounts    types.MountPolicies `json:"mounts,omitempty"        yaml:"mounts,omitempty"`
	ReadOnly  bool                `json:"read_only,omitempty"     yaml:"read_only,omitempty"`
}

func NewInfo(ctx context.Context, env map[string]string, ftpPort, sftpPort uint16, localDir, remoteDir, podIP string, mounts types.MountPolicies, ro bool) *Info {
	var port uint16
	if client.GetConfig(ctx).Intercept().UseFtp {
		port = ftpPort
	} else {
		port = sftpPort
	}
	if mounts == nil {
		// Older traffic-managers will not provide the mount-path -> mount-policy map, so we must
		// create it from the TELEPRESENCE_MOUNTS environment.
		if tpMounts := env["TELEPRESENCE_MOUNTS"]; tpMounts != "" {
			// This is a Unix path, so we cannot use filepath.SplitList
			paths := strings.Split(tpMounts, ":")
			mounts = make(types.MountPolicies, len(paths))
			mp := types.MountPolicyRemote
			if ro {
				mp = types.MountPolicyRemoteReadOnly
			}
			for _, path := range paths {
				if path == "/tmp" && mp == types.MountPolicyRemoteReadOnly {
					// A read-only mount of /tmp is probably pointless
					mounts[path] = types.MountPolicyLocal
				} else {
					mounts[path] = mp
				}
			}
		}
	}
	return &Info{
		LocalDir:  localDir,
		RemoteDir: remoteDir,
		PodIP:     podIP,
		Port:      port,
		Mounts:    mounts,
		ReadOnly:  ro,
	}
}
