package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

type Config interface {
	Ext() agentconfig.SidecarExt
	AgentConfig() *agentconfig.Sidecar
	HasMounts(ctx context.Context) bool
	PodName() string
	PodIP() string
}

type config struct {
	sidecarExt agentconfig.SidecarExt
	podName    string
	podIP      string
}

func LoadConfig(ctx context.Context) (Config, error) {
	bs, err := dos.ReadFile(ctx, filepath.Join(agentconfig.ConfigMountPoint, agentconfig.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("unable to open agent ConfigMap: %w", err)
	}

	c := config{}
	c.sidecarExt, err = agentconfig.UnmarshalYAML(bs)
	if err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	sc := c.AgentConfig()
	if sc.LogLevel != "" {
		// Override default from environment
		log.SetLevel(ctx, sc.LogLevel)
	}
	if sc.ManagerPort == 0 {
		sc.ManagerPort = 8081
	}
	c.podName = dos.Getenv(ctx, "_TEL_AGENT_NAME")
	c.podIP = dos.Getenv(ctx, "_TEL_AGENT_POD_IP")
	for _, cn := range sc.Containers {
		if err := addAppMounts(ctx, cn); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func (c *config) HasMounts(ctx context.Context) bool {
	for _, cn := range c.AgentConfig().Containers {
		if len(cn.Mounts) > 0 {
			return true
		}
	}
	return false
}

func (c *config) Ext() agentconfig.SidecarExt {
	return c.sidecarExt
}

func (c *config) AgentConfig() *agentconfig.Sidecar {
	return c.sidecarExt.AgentConfig()
}

func (c *config) PodName() string {
	return c.podName
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

	if appMountsDir, err := dos.Open(ctx, ag.MountPoint); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
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
	}
	return mountVRS(ctx, ag, cnMountPoint)
}

func mountVRS(ctx context.Context, ag *agentconfig.Container, cnMountPoint string) error {
	const vrsDir = "/var/run/secrets"
	// Capture /var/run/secrets subdirs that has been injected but not added by
	// the injector. That might be because the injector is older than 2.13.3 or
	// because the injectors reinvocationStrategy is set to "Never".
	var vrsMounts []string
	vrs, err := dos.ReadDir(ctx, vrsDir)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return err
	}

	hasMount := func(m string) bool {
		for _, em := range ag.Mounts {
			if strings.HasPrefix(em, m) {
				return true
			}
		}
		return false
	}
	anns, err := readAnnotations(ctx)
	if err != nil {
		dlog.Warnf(ctx, "failed to read annotations: %v", err)
	}

	ignored := agentconfig.GetIgnoredVolumeMounts(anns)
	for _, vr := range vrs {
		if vr.IsDir() {
			subDir := filepath.Join(vrsDir, vr.Name())
			if !hasMount(subDir) && !ignored.IsVolumeIgnored("", subDir) {
				ag.Mounts = append(ag.Mounts, subDir)
				vrsMounts = append(vrsMounts, subDir)
			}
		}
	}
	if len(vrsMounts) == 0 {
		return nil
	}

	vrsExportDir := filepath.Join(cnMountPoint, vrsDir)
	if err := dos.MkdirAll(ctx, vrsExportDir, 0o700); err != nil {
		return err
	}
	for _, mount := range vrsMounts {
		newName := filepath.Join(vrsExportDir, filepath.Base(mount))
		if err = dos.Symlink(ctx, mount, newName); err != nil {
			dlog.Warnf(ctx, "can't symlink %s to %s", mount, newName)
		}
	}
	return nil
}

func readAnnotations(ctx context.Context) (map[string]string, error) {
	af, err := dos.Open(ctx, filepath.Join(agentconfig.AnnotationMountPoint, "annotations"))
	if err != nil {
		return nil, err
	}
	defer af.Close()
	r := bufio.NewScanner(af)
	m := make(map[string]string)
	for r.Scan() {
		vs := strings.SplitN(r.Text(), "=", 2)
		if len(vs) == 2 {
			av := vs[1]
			if uq, err := strconv.Unquote(av); err == nil {
				av = uq
			}
			m[vs[0]] = av
		}
	}
	return m, nil
}
