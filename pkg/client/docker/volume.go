package docker

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const TelemountPlugin = "datawire/telemount:" + runtime.GOARCH

// EnsureVolumePlugin checks if the datawire/telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureVolumePlugin(ctx context.Context) error {
	cli := GetClient(ctx)
	pi, _, err := cli.PluginInspectWithRaw(ctx, TelemountPlugin)
	if err != nil {
		if !client.IsErrNotFound(err) {
			dlog.Errorf(ctx, "docker plugin inspect: %v", err)
		}
		return installVolumePlugin(ctx)
	}
	if !pi.Enabled {
		err = cli.PluginEnable(ctx, TelemountPlugin, types.PluginEnableOptions{Timeout: 5})
	}
	return err
}

func installVolumePlugin(ctx context.Context) error {
	cmd := proc.CommandContext(ctx, "docker", "plugin", "install", "--grant-all-permissions", TelemountPlugin, "DEBUG=true")
	_, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		err = fmt.Errorf("docker plugin install %s: %w", TelemountPlugin, err)
	}
	return err
}

func StartVolumeMounts(ctx context.Context, dcName, container string, sftpPort int32, mounts, vols []string) ([]string, error) {
	host, err := ContainerIP(ctx, dcName)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieved container ip for %s: %w", dcName, err)
	}
	for i, dir := range mounts {
		v := fmt.Sprintf("%s-%d", container, i)
		if err := startVolumeMount(ctx, host, sftpPort, v, container, dir); err != nil {
			return vols, err
		}
		vols = append(vols, v)
	}
	return vols, nil
}

func StopVolumeMounts(ctx context.Context, vols []string) {
	for _, vol := range vols {
		if err := stopVolumeMount(ctx, vol); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func startVolumeMount(ctx context.Context, host string, port int32, volumeName, container, dir string) error {
	_, err := GetClient(ctx).VolumeCreate(ctx, volume.CreateOptions{
		Driver: TelemountPlugin,
		DriverOpts: map[string]string{
			"host":      host,
			"container": container,
			"port":      strconv.Itoa(int(port)),
			"dir":       dir,
		},
		Name: volumeName,
	})
	if err != nil {
		err = fmt.Errorf("docker volume create %d %s %s: %w", port, container, dir, err)
	}
	return err
}

func stopVolumeMount(ctx context.Context, volume string) error {
	err := GetClient(ctx).VolumeRemove(ctx, volume, false)
	if err != nil {
		err = fmt.Errorf("docker volume rm %s: %w", volume, err)
	}
	return err
}

// ContainerIP returns the IP assigned to the container with the given name on the telepresence network.
func ContainerIP(ctx context.Context, name string) (string, error) {
	ci, err := GetClient(ctx).ContainerInspect(ctx, name)
	if err != nil {
		return "", fmt.Errorf("docker container inspect %s: %w", "userd", err)
	}
	if ns := ci.NetworkSettings; ns != nil {
		if tn, ok := ns.Networks["telepresence"]; ok {
			return tn.IPAddress, nil
		}
	}
	return "", os.ErrNotExist
}
