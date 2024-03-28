package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
	dockerClient "github.com/docker/docker/client"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// EnsureVolumePlugin checks if the datawire/telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureVolumePlugin(ctx context.Context) (string, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return "", err
	}
	cfg := client.GetConfig(ctx).Intercept().Telemount
	pn := pluginName(ctx)
	if pt := cfg.Tag; pt != "" {
		pn += "-" + pt
	} else if lv, err := latestPluginVersion(ctx, pn); err == nil {
		pn += "-" + lv.String()
	} else {
		dlog.Warnf(ctx, "failed to get latest version of docker volume plugin %s: %v", pn, err)
	}
	pi, _, err := cli.PluginInspectWithRaw(ctx, pn)
	if err != nil {
		if !dockerClient.IsErrNotFound(err) {
			dlog.Errorf(ctx, "docker plugin inspect: %v", err)
		}
		return pn, installVolumePlugin(ctx, pn)
	}
	if !pi.Enabled {
		err = cli.PluginEnable(ctx, pn, types.PluginEnableOptions{Timeout: 5})
	}
	return pn, err
}

func pluginName(ctx context.Context) string {
	tm := client.GetConfig(ctx).Intercept().Telemount
	return fmt.Sprintf("%s/%s/%s:%s", tm.Registry, tm.Namespace, tm.Repository, runtime.GOARCH)
}

func installVolumePlugin(ctx context.Context, pluginName string) error {
	dlog.Debugf(ctx, "Installing docker volume plugin %s", pluginName)
	cmd := proc.CommandContext(ctx, "docker", "plugin", "install", "--grant-all-permissions", pluginName)
	_, err := proc.CaptureErr(cmd)
	if err != nil {
		err = fmt.Errorf("docker plugin install %s: %w", pluginName, err)
	}
	return err
}

type pluginInfo struct {
	LatestVersion string `json:"latestVersions"`
	LastCheck     int64  `json:"lastCheck"`
}

const pluginInfoMaxAge = 24 * time.Hour

func latestPluginVersion(ctx context.Context, pluginName string) (ver semver.Version, err error) {
	file := "volume-plugin-info.json"
	pi := pluginInfo{}
	if err = cache.LoadFromUserCache(ctx, &pi, file); err != nil {
		if !os.IsNotExist(err) {
			return ver, err
		}
		pi.LastCheck = 0
	}

	now := time.Now().UnixNano()
	if time.Duration(now-pi.LastCheck) > pluginInfoMaxAge {
		ver, err = getLatestPluginVersion(ctx, pluginName)
		if err == nil {
			pi.LatestVersion = ver.String()
			pi.LastCheck = now
			err = cache.SaveToUserCache(ctx, &pi, file, cache.Public)
		}
	} else {
		dlog.Debugf(ctx, "Using cached version %s for %s", pi.LatestVersion, pluginName)
		ver, err = semver.Parse(pi.LatestVersion)
	}
	return ver, err
}

type imgResult struct {
	Name string `json:"name"`
}
type repsResponse struct {
	Results []imgResult `json:"results"`
}

func getLatestPluginVersion(ctx context.Context, pluginName string) (ver semver.Version, err error) {
	dlog.Debugf(ctx, "Checking for latest version of %s", pluginName)
	cfg := client.GetConfig(ctx).Intercept().Telemount
	url := fmt.Sprintf("https://%s/namespaces/%s/repositories/%s/tags", cfg.RegistryAPI, cfg.Namespace, cfg.Repository)
	var rq *http.Request
	rq, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ver, err
	}
	rq.Header.Add("Accept", "application/json")
	var rs *http.Response
	rs, err = http.DefaultClient.Do(rq)
	if err != nil {
		return ver, err
	}
	var data []byte
	data, err = io.ReadAll(rs.Body)
	if err != nil {
		return ver, err
	}
	_ = rs.Body.Close()
	if rs.StatusCode != http.StatusOK {
		return ver, errors.New(rs.Status)
	}
	var infos repsResponse
	err = json.Unmarshal(data, &infos)
	if err != nil {
		return ver, err
	}
	pfx := runtime.GOARCH + "-"
	for _, info := range infos.Results {
		if strings.HasPrefix(info.Name, pfx) {
			iv, err := semver.Parse(strings.TrimPrefix(info.Name, pfx))
			if err == nil && iv.GT(ver) {
				ver = iv
			}
		}
	}
	dlog.Debugf(ctx, "Found latest version of %s to be %s", pluginName, ver)
	return ver, err
}

func StartVolumeMounts(ctx context.Context, pluginName, dcName, container string, sftpPort int32, mounts, vols []string) ([]string, error) {
	host, err := ContainerIP(ctx, dcName)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieved container ip for %s: %w", dcName, err)
	}
	for i, dir := range mounts {
		v := fmt.Sprintf("%s-%d", container, i)
		if err := startVolumeMount(ctx, pluginName, host, sftpPort, v, container, dir); err != nil {
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

func startVolumeMount(ctx context.Context, pluginName, host string, port int32, volumeName, container, dir string) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	_, err = cli.VolumeCreate(ctx, volume.CreateOptions{
		Driver: pluginName,
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

// ContainerIP returns the IP assigned to the container with the given name on the telepresence network.
func ContainerIP(ctx context.Context, name string) (string, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return "", err
	}
	ci, err := cli.ContainerInspect(ctx, name)
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
