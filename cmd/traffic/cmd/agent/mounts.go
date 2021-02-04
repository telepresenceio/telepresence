package agent

import (
	"regexp"

	"github.com/docker/docker/pkg/mount"
)

// mountPoints returns a filtered list mount-points. The filter is a logical copy of the IGNORED_MOUNTS found in
// tel-1's k8s-proxy/podinfo.py. It will ignore the following
//  /, /sys, /proc, /dev, /etc/hostname, /etc/resolv.conf, or /etc/hosts
// and everything that starts with:
//  /sys/, /proc/, or /dev/
func mountPoints() ([]string, error) {
	ignoredMounts := regexp.MustCompile(`\A/(?:(?:(?:sys|proc|dev)(?:/.*)?)|etc/(?:hostname|resolv\.conf|hosts))?\z`)

	mountInfos, err := mount.GetMounts(func(info *mount.Info) (bool, bool) {
		return ignoredMounts.MatchString(info.Mountpoint), false
	})
	if err != nil {
		return nil, err
	}
	mounts := make([]string, len(mountInfos))
	for i, mi := range mountInfos {
		mounts[i] = mi.Mountpoint
	}
	return mounts, nil
}
