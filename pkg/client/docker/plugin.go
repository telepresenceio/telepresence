package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/containerd/errdefs"
	dockerTypes "github.com/docker/docker/api/types"
	"github.com/go-json-experiment/json"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const (
	pluginTypeVolume  = "volume"
	pluginTypeNetwork = "network"
)

// EnsureVolumePlugin checks if the telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureVolumePlugin(ctx context.Context) (string, error) {
	cfg := client.DockerImage(client.GetConfig(ctx).Docker().Telemount)
	return ensurePlugin(ctx, pluginTypeVolume, &cfg)
}

// EnsureNetworkPlugin checks if the telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureNetworkPlugin(ctx context.Context) (string, error) {
	cfg := client.DockerImage(client.GetConfig(ctx).Docker().Teleroute)
	return ensurePlugin(ctx, pluginTypeNetwork, &cfg)
}

func NetworkPluginName(ctx context.Context) string {
	cfg := client.DockerImage(client.GetConfig(ctx).Docker().Teleroute)
	return latestPluginName(ctx, &cfg, pluginTypeNetwork)
}

func ensurePlugin(ctx context.Context, pluginType string, cfg *client.DockerImage) (string, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return "", err
	}
	pn := latestPluginName(ctx, cfg, pluginType)
	pi, _, err := cli.PluginInspectWithRaw(ctx, pn)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			dlog.Errorf(ctx, "docker plugin inspect: %v", err)
		}
		return pn, installPlugin(ctx, pn)
	}
	if !pi.Enabled {
		err = cli.PluginEnable(ctx, pn, dockerTypes.PluginEnableOptions{Timeout: 5})
	}
	dlog.Debugf(ctx, "using %s plugin: %s", pluginType, pn)
	return pn, err
}

func pluginName(tm *client.DockerImage) string {
	return fmt.Sprintf("%s/%s/%s:%s", tm.Registry, tm.Namespace, tm.Repository, runtime.GOARCH)
}

func latestPluginName(ctx context.Context, cfg *client.DockerImage, pluginType string) string {
	pn := pluginName(cfg)
	if pt := cfg.Tag; pt != "" {
		pn += "-" + pt
	} else if lv, err := latestPluginVersion(ctx, pn, pluginType, cfg); err == nil {
		pn += "-" + lv.String()
	} else {
		dlog.Warnf(ctx, "failed to get latest version of docker %s plugin %s: %v", pluginType, pn, err)
	}
	return pn
}

func installPlugin(ctx context.Context, pluginName string) error {
	dlog.Debugf(ctx, "Installing docker plugin %s", pluginName)
	cmd := proc.CommandContext(ctx, Exe, "plugin", "install", "--grant-all-permissions", pluginName)
	_, err := proc.CaptureErr(cmd)
	if err != nil {
		err = fmt.Errorf("%s plugin install %s: %w", Exe, pluginName, err)
	}
	return err
}

type pluginInfo struct {
	LatestVersion string `json:"latestVersions"`
	LastCheck     int64  `json:"lastCheck"`
}

const pluginInfoMaxAge = 24 * time.Hour

var zeroVersion = semver.Version{} //nolint:gochecknoglobals // constant

func latestPluginVersion(ctx context.Context, pluginName, pluginType string, cfg *client.DockerImage) (ver semver.Version, err error) {
	file := pluginType + "-plugin-info.json"
	pi := pluginInfo{}
	if err = cache.LoadFromUserCache(ctx, &pi, file); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return ver, err
		}
		pi.LastCheck = 0
	}

	now := time.Now().UnixNano()
	if time.Duration(now-pi.LastCheck) > pluginInfoMaxAge {
		ver, err = getLatestPluginVersion(ctx, pluginName, cfg)
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

func getLatestPluginVersion(ctx context.Context, pluginName string, cfg *client.DockerImage) (ver semver.Version, err error) {
	dlog.Debugf(ctx, "Checking for latest version of %s", pluginName)
	tag := cfg.Tag
	if tag == "debug" {
		return zeroVersion, nil
	}
	if tag != "" {
		return semver.Parse(tag)
	}
	if cfg.RegistryAPI == "ghcr.io/v2" {
		return ver, fmt.Errorf("a tag for plugin %s must be set the client's docker config because the ghcr.io/v2 registry does not support anonymous queries", pluginName)
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
