package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

type Config interface {
	AgentConfig() *agentconfig.Sidecar
	HasMounts(ctx context.Context) bool
	PodIP() string
}

type config struct {
	agentconfig.Sidecar
	podIP string
}

func LoadConfig(ctx context.Context) (Config, error) {
	bs, err := dos.ReadFile(ctx, filepath.Join(agentconfig.ConfigMountPoint, agentconfig.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("unable to open agent ConfigMap: %w", err)
	}

	c := config{}
	if err = yaml.Unmarshal(bs, &c.Sidecar); err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	if c.LogLevel != "" {
		// Override default from environment
		log.SetLevel(ctx, c.LogLevel)
	}
	if c.ManagerPort == 0 {
		c.ManagerPort = 8081
	}
	c.podIP = dos.Getenv(ctx, "_TEL_AGENT_POD_IP")
	for _, cn := range c.Containers {
		if err := addAppMounts(ctx, cn); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func (c *config) AgentConfig() *agentconfig.Sidecar {
	return &c.Sidecar
}

func (c *config) HasMounts(ctx context.Context) bool {
	for _, cn := range c.Containers {
		if len(cn.Mounts) > 0 {
			return true
		}
	}
	return false
}

func (c *config) PodIP() string {
	return c.podIP
}

func OtelResources(ctx context.Context, c Config) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Bool("tel2.has-mounts", c.HasMounts(ctx)),
		attribute.String("tel2.workload-name", c.AgentConfig().WorkloadName),
		attribute.String("tel2.workload-kind", c.AgentConfig().WorkloadKind),
		attribute.String("tel2.manager-host", fmt.Sprintf("%s:%v", c.AgentConfig().ManagerHost, c.AgentConfig().ManagerPort)),
		attribute.Bool("tel2.manual", c.AgentConfig().Manual),
		attribute.String("k8s.namespace", c.AgentConfig().Namespace),
		attribute.String("k8s.pod-ip", c.PodIP()),
	}
}

// addAppMounts adds each of the mounts present under the containers MountPoint as a
// symlink under the agentconfig.ExportsMountPoint/<container mount>/.
func addAppMounts(ctx context.Context, ag *agentconfig.Container) error {
	dlog.Infof(ctx, "Adding exported mounts for container %s", ag.Name)
	cnMountPoint := filepath.Join(agentconfig.ExportsMountPoint, filepath.Base(ag.MountPoint))
	if err := dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		dlog.Infof(ctx, "The directory %q already exists. Container restarted?", cnMountPoint)
		if err = dos.RemoveAll(ctx, cnMountPoint); err != nil {
			return err
		}
		if err = dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
			return err
		}
	}

	volPaths := dos.Getenv(ctx, ag.EnvPrefix+agentconfig.EnvInterceptMounts)
	if len(volPaths) > 0 {
		ag.Mounts = slice.AppendUnique(ag.Mounts, strings.Split(volPaths, ":")...)
	}

	appMountsDir, err := dos.Open(ctx, ag.MountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil // Nothing is mounted from this app container. That's ok
		}
		return err
	}

	defer appMountsDir.Close()
	mounts, err := appMountsDir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if err = dos.Symlink(ctx, filepath.Join(ag.MountPoint, mount.Name()), filepath.Join(cnMountPoint, mount.Name())); err != nil {
			return err
		}
	}
	return nil
}
