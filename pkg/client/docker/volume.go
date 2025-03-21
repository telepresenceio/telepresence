package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	dockerTypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
	dockerClient "github.com/docker/docker/client"
	"github.com/go-json-experiment/json"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// EnsureVolumePlugin checks if the telemount plugin is installed and installs it if that is
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
		err = cli.PluginEnable(ctx, pn, dockerTypes.PluginEnableOptions{Timeout: 5})
	}
	dlog.Debugf(ctx, "using volume plugin: %s", pn)
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

var zeroVersion = semver.Version{} //nolint:gochecknoglobals // constant

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
		if err == nil && !ver.EQ(zeroVersion) {
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
	if cfg.RegistryAPI == "ghcr.io/v2" {
		// This registryAPI have on support for anonymous queries, so we hardcode a default for the 0.1.6 version here for now.
		tag := cfg.Tag
		if tag == "" {
			tag = "0.1.6"
		}
		if tag == "debug" {
			return zeroVersion, nil
		}
		return semver.Parse(tag)
	}
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

// CreateVolumes creates the volumes necessary when mounting volumes required by when engaging the remote container.
// The sftpPort is the port where the daemon container provides access to the remote sftp-server.
// The mounts are provided as a map of mount policies keyed by paths.
// Each volume is given the name of the remote container suffixed by a dash and a sequence number, starting at 0.
// Returns a map of paths keyed by volume names.
func CreateVolumes(
	ctx context.Context,
	daemonContainer string,
	sftpPort uint16,
	remoteContainer string,
	mounts types.MountPolicies,
	ro bool,
) (map[string]string, error) {
	var host, plugin string
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
				host, err = ContainerIP(ctx, daemonContainer)
				if err != nil {
					return nil, fmt.Errorf("failed to retrieved remoteContainer ip for %s: %w", daemonContainer, err)
				}
				plugin, err = EnsureVolumePlugin(ctx)
				if err != nil {
					ioutil.Printf(output.Err(ctx), "Remote mount disabled: %s\n", err)
					return nil, nil
				}
			}
			v := fmt.Sprintf("%s-%d", remoteContainer, i)
			i++
			if err = createVolume(ctx, plugin, host, sftpPort, v, remoteContainer, dir, volRO); err != nil {
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
			dlog.Error(ctx, err)
		}
	}
}

func createVolume(ctx context.Context, pluginName, host string, port uint16, volumeName, container, dir string, ro bool) error {
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	opts := map[string]string{
		"host":      host,
		"container": container,
		"port":      strconv.Itoa(int(port)),
		"dir":       dir,
	}
	if ro {
		var ver *semver.Version
		if di := strings.LastIndexByte(pluginName, '-'); di > 0 {
			tag := pluginName[di+1:]
			if v, err := semver.Parse(tag); err == nil {
				ver = &v
			}
		}
		if ver != nil && ver.LT(semver.MustParse("0.1.6")) {
			dlog.Warnf(ctx, "The %q docker volume plugin does not support read-only mode. Please upgrade to a more recent version", pluginName)
		} else {
			opts["ro"] = "true"
		}
	}

	dlog.Debugf(ctx, "VolumeCreate(%s, %s, %s)", pluginName, opts, volumeName)
	_, err = cli.VolumeCreate(ctx, volume.CreateOptions{
		Driver:     pluginName,
		DriverOpts: opts,
		Name:       volumeName,
	})
	if err != nil {
		err = fmt.Errorf("docker volume create %d %s %s: %w", port, container, dir, err)
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
