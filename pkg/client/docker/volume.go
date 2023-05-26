package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const TelemountPlugin = "datawire/telemount:" + runtime.GOARCH

// EnsureVolumePlugin checks if the datawire/telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureVolumePlugin(ctx context.Context) error {
	cmd := proc.CommandContext(ctx, "docker", "plugin", "inspect", "--format", "{{.Enabled}}", TelemountPlugin)
	out, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		if !strings.Contains(err.Error(), "not found") {
			dlog.Errorf(ctx, "docker plugin inspect: %v", err)
		}
		return installVolumePlugin(ctx)
	}
	if strings.TrimSpace(string(out)) == "true" {
		return nil
	}
	return enableVolumePlugin(ctx)
}

func enableVolumePlugin(ctx context.Context) error {
	cmd := proc.CommandContext(ctx, "docker", "plugin", "enable", "--timeout", "5", TelemountPlugin)
	_, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		err = fmt.Errorf("docker plugin enable %s: %w", TelemountPlugin, err)
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

func StartVolumeMounts(ctx context.Context, dcName, container string, sftpPort int32, mounts, vols, args []string) ([]string, []string, error) {
	host, err := ContainerIP(ctx, dcName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to retrieved container ip for %s: %w", dcName, err)
	}
	for i, dir := range mounts {
		v := fmt.Sprintf("%s-%d", container, i)
		if err := startVolumeMount(ctx, host, sftpPort, v, container, dir); err != nil {
			return vols, args, err
		}
		vols = append(vols, v)
		args = append(args, "-v", fmt.Sprintf("%s:%s", v, dir))
	}
	return vols, args, nil
}

func StopVolumeMounts(ctx context.Context, vols []string) {
	for _, vol := range vols {
		if err := stopVolumeMount(ctx, vol); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func startVolumeMount(ctx context.Context, host string, port int32, volume, container, dir string) error {
	cmd := proc.CommandContext(ctx, "docker", "volume", "create",
		"-d", TelemountPlugin,
		"-o", "host="+host,
		"-o", fmt.Sprintf("port=%d", port),
		"-o", "container="+container,
		"-o", "dir="+dir,
		volume)
	_, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		err = fmt.Errorf("docker volume create %d %s %s: %w", port, container, dir, err)
	}
	return err
}

func stopVolumeMount(ctx context.Context, volume string) error {
	cmd := proc.CommandContext(ctx, "docker", "volume", "rm", volume)
	_, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		err = fmt.Errorf("docker volume rm %s: %w", volume, err)
	}
	return err
}

// ContainerIP returns the IP assigned to the container with the given name on the telepresence network.
func ContainerIP(ctx context.Context, name string) (string, error) {
	cmd := proc.CommandContext(ctx, "docker", "container", "inspect", name,
		"--format", "{{ range $key, $value := .NetworkSettings.Networks }}{{ $key }}:{{ $value.IPAddress }}\n{{ end }}")
	out, err := proc.CaptureErr(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("docker container inspect %s: %w", "userd", err)
	}
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		e := strings.Split(s.Text(), ":")
		if len(e) == 2 && e[0] == "telepresence" {
			return e[1], nil
		}
	}
	return "", os.ErrNotExist
}
