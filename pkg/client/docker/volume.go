package docker

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/blang/semver/v4"
	"github.com/docker/docker/api/types/volume"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// CreateVolumes creates the volumes necessary when mounting volumes required by when engaging the remote container.
// The hostPort is the <daemon ip>/<sftp port> where the access to the remote sftp-server is provided.
// The mounts are provided as a map of mount policies keyed by paths.
// Each volume is given the name of the remote container suffixed by a dash and a sequence number, starting at 0.
// Returns a map of paths keyed by volume names.
func CreateVolumes(
	ctx context.Context,
	hostPort netip.AddrPort,
	remoteContainer string,
	mounts types.MountPolicies,
	ro bool,
) (map[string]string, error) {
	var plugin string
	vols := make(map[string]string)
	i := 0
	for dir, policy := range mounts {
		volRO := ro
		switch policy {
		case types.MountPolicyIgnore:
			continue
		case types.MountPolicyLocal:
			// Mount using a local binding, unless user already provided a mount.
		case types.MountPolicyRemoteReadOnly:
			volRO = true
			fallthrough
		case types.MountPolicyRemote:
			var err error
			if plugin == "" {
				plugin, err = EnsureVolumePlugin(ctx)
				if err != nil {
					ioutil.Printf(output.Err(ctx), "Remote mount disabled: %s\n", err)
					return nil, nil
				}
			}
			v := fmt.Sprintf("%s-%d", remoteContainer, i)
			i++
			if err = createVolume(ctx, plugin, hostPort, v, remoteContainer, dir, volRO); err != nil {
				return vols, err
			}
			vols[v] = dir
		}
	}
	return vols, nil
}

func RemoveVolumes(ctx context.Context, vols []string) {
	for _, vol := range vols {
		if err := removeVolume(ctx, vol); err != nil {
			clog.Error(ctx, err)
		}
	}
}

func VolumeDriverOpts(ctx context.Context, pluginName string, hostPort netip.AddrPort, volumeName, container, dir string, ro bool) map[string]string {
	opts := map[string]string{
		"host":      hostPort.Addr().String(),
		"container": container,
		"port":      strconv.Itoa(int(hostPort.Port())),
		"dir":       dir,
	}
	if ro {
		ver := parsePluginSemver(pluginName)
		if ver != nil && ver.LT(semver.MustParse("0.1.6")) {
			clog.Warnf(ctx, "The %q docker volume plugin does not support read-only mode. Please upgrade to a more recent version", pluginName)
		} else {
			opts["ro"] = "true"
		}
	}
	return opts
}

// parsePluginSemver extracts a semantic version from a plugin name formatted like "name-<semver>".
func parsePluginSemver(name string) *semver.Version {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		tag := name[i+1:]
		if v, err := semver.Parse(tag); err == nil {
			return &v
		}
	}
	return nil
}

func createVolume(ctx context.Context, pluginName string, hostPort netip.AddrPort, volumeName, container, dir string, ro bool) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	opts := VolumeDriverOpts(ctx, pluginName, hostPort, volumeName, container, dir, ro)

	clog.Debugf(ctx, "VolumeCreate(%s, %s, %s)", pluginName, opts, volumeName)
	_, err = cli.VolumeCreate(ctx, volume.CreateOptions{
		Driver:     pluginName,
		DriverOpts: opts,
		Name:       volumeName,
	})
	if err != nil {
		err = fmt.Errorf("docker volume create %s %s %s: %w", hostPort, container, dir, err)
	}
	return err
}

func removeVolume(ctx context.Context, volume string) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	err = cli.VolumeRemove(ctx, volume, false)
	if err != nil {
		err = fmt.Errorf("docker volume rm %s: %w", volume, err)
	}
	return err
}
