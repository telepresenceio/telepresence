package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	dockerTypes "github.com/docker/docker/api/types"
	network2 "github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
	"github.com/go-json-experiment/json"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// EnsureVolumePlugin checks if the telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureVolumePlugin(ctx context.Context) (string, error) {
	cfg := client.DockerImage(client.GetConfig(ctx).Intercept().Telemount)
	return ensurePlugin(ctx, "volume", &cfg)
}

// EnsureNetworkPlugin checks if the telemount plugin is installed and installs it if that is
// not the case. The plugin is also enabled.
func EnsureNetworkPlugin(ctx context.Context) (string, error) {
	cfg := client.DockerImage(client.GetConfig(ctx).Intercept().Teleroute)
	return ensurePlugin(ctx, "network", &cfg)
}

func ensurePlugin(ctx context.Context, pluginType string, cfg *client.DockerImage) (string, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return "", err
	}
	pn := latestPluginName(ctx, cfg, pluginType)
	pi, _, err := cli.PluginInspectWithRaw(ctx, pn)
	if err != nil {
		if !dockerClient.IsErrNotFound(err) {
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
	if cfg.RegistryAPI == "ghcr.io/v2" {
		// This registryAPI has on support for anonymous queries, so we hardcode a default for the 0.1.6 version here for now.
		tag := cfg.Tag
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

type ContainerInfo struct {
	ID   string
	Name string
	Pid  int
	IP   netip.Addr
}

// GetContainerInfo returns the name and process ID of the container with the given ID along with its associated IP in the given network.
func GetContainerInfo(ctx context.Context, cid string, network string) (*ContainerInfo, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return nil, err
	}
	ci, err := cli.ContainerInspect(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("docker container inspect %s: %w", "userd", err)
	}

	if ns := ci.NetworkSettings; ns != nil {
		if network == "" {
			nts, err := cli.NetworkList(ctx, network2.ListOptions{})
			if err != nil {
				return nil, fmt.Errorf("docker network list %s: %w", cid, err)
			}
			cfg := client.DockerImage(client.GetConfig(ctx).Intercept().Teleroute)
			pluginNamePrefix := pluginName(&cfg)
			for _, n := range nts {
				if strings.HasPrefix(n.Driver, pluginNamePrefix) {
					if _, ok := n.Containers[ci.ID]; ok {
						network = n.Name
						break
					}
				}
			}
		}
		if tn, ok := ns.Networks[network]; ok {
			addr, err := netip.ParseAddr(tn.IPAddress)
			if err != nil {
				return nil, err
			}
			return &ContainerInfo{ID: ci.ID, Pid: ci.State.Pid, IP: addr, Name: ci.Name}, nil
		}
	}
	return nil, os.ErrNotExist
}
