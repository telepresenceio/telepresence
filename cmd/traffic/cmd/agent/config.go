package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Config interface {
	AgentConfig() *agentconfig.Sidecar
	Annotations() map[string]string
	HasRemoteMounts() bool
	PodName() string
	PodIP() netip.Addr
	PodUID() k8sTypes.UID
}

type config struct {
	sidecar     *agentconfig.Sidecar
	annotations map[string]string
	podName     string
	podIP       netip.Addr
	podUID      k8sTypes.UID
}

func LoadConfig(ctx context.Context) (Config, error) {
	var cfgTight string
	var ok bool
	c := config{}

	annFile := filepath.Join(agentconfig.PodInfoMountPath, "annotations")
	annData, err := dos.ReadFile(ctx, annFile)
	if err != nil {
		// Was an older traffic-manager in charge of injecting this agent so that the config can be found from the environment?
		dlog.Warnf(ctx, "Unable to read annotations from %s: %v", annFile, err)
		dlog.Warnf(ctx, "Loading agent config from env %s", agentconfig.EnvAgentConfig)
		cfgTight, ok = dos.LookupEnv(ctx, agentconfig.EnvAgentConfig)
	} else {
		dlog.Infof(ctx, "Loading agent config from %s", annFile)
		c.annotations, err = readMap(string(annData))
		if err != nil {
			return nil, fmt.Errorf("unable to parse annotations from %s: %v", annFile, err)
		}
		cfgTight, ok = c.annotations[annotation.Config]
	}
	if !ok {
		return nil, errors.New("unable to retrieve agent config")
	}

	c.sidecar, err = agentconfig.UnmarshalJSON(cfgTight)
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
	if sc.ClientConnectionTTL == 0 {
		sc.ClientConnectionTTL = 24 * time.Hour
	}
	c.podName, ok = dos.LookupEnv(ctx, "_TEL_AGENT_NAME")
	if !ok {
		return nil, errors.New("missing NAME")
	}
	if podIPStr, ok := dos.LookupEnv(ctx, "_TEL_AGENT_POD_IP"); !ok {
		return nil, errors.New("missing POD_IP")
	} else if podIP, err := netip.ParseAddr(podIPStr); err != nil {
		return nil, fmt.Errorf("invalid POD_IP: %w", err)
	} else {
		c.podIP = podIP
	}
	podUID, ok := dos.LookupEnv(ctx, "_TEL_AGENT_POD_UID")
	if !ok {
		return nil, errors.New("missing POD_UID")
	}
	c.podUID = k8sTypes.UID(podUID)
	for _, cn := range sc.Containers {
		err = addAppMounts(ctx, sc.MountPolicies, cn)
		if err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func (c *config) PodUID() k8sTypes.UID {
	return c.podUID
}

func (c *config) HasRemoteMounts() bool {
	for _, cn := range c.AgentConfig().Containers {
		for _, p := range cn.Mounts {
			if p == types.MountPolicyRemote || p == types.MountPolicyRemoteReadOnly {
				return true
			}
		}
	}
	return false
}

func (c *config) Annotations() map[string]string {
	return c.annotations
}

func (c *config) AgentConfig() *agentconfig.Sidecar {
	return c.sidecar
}

func (c *config) PodName() string {
	return c.podName
}

func (c *config) PodIP() netip.Addr {
	return c.podIP
}

// addAppMounts adds each of the mounts present under the containers MountPoint as a
// symlink under the agentconfig.ExportsMountPoint/<container mount>/.
// Returns MountPolicies keyed by the full path of each mount.
func addAppMounts(ctx context.Context, mps types.MountPolicies, ag *agentconfig.Container) error {
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

	if appMountsDir, err := dos.Open(ctx, ag.MountPoint); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else {
		defer appMountsDir.Close()
		mounts, err := appMountsDir.ReadDir(-1)
		if err != nil {
			return err
		}
		for _, mount := range mounts {
			switch mps.Get("", "/"+mount.Name()) {
			case types.MountPolicyIgnore, types.MountPolicyLocal:
			default:
				subDir := filepath.Join(ag.MountPoint, mount.Name())
				if err = dos.Symlink(ctx, subDir, filepath.Join(cnMountPoint, mount.Name())); err != nil {
					return err
				}
			}
		}
	}
	if err := mountVRS(ctx, mps, ag, cnMountPoint); err != nil {
		return err
	}

	// Verify that all mounts exists, so that the client doesn't attempt to mount nonexistent paths
	for path, policy := range ag.Mounts {
		mp := filepath.Join(cnMountPoint, path)
		if policy == types.MountPolicyRemote || policy == types.MountPolicyRemoteReadOnly {
			_, err := dos.Stat(ctx, mp)
			if err != nil {
				dlog.Infof(ctx, "Failed to stat %q. It will not be exported: %v", mp, err)
				delete(ag.Mounts, path)
			}
		}
	}
	return nil
}

func mountVRS(ctx context.Context, mps types.MountPolicies, ag *agentconfig.Container, cnMountPoint string) error {
	const vrsDir = "/var/run/secrets"
	// Capture /var/run/secrets subdirs that has been injected but not added by the injector.
	vrs, err := dos.ReadDir(ctx, vrsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return err
	}

	vrsExportDir := filepath.Join(cnMountPoint, vrsDir)
	hasVrsExportDir := false
	for _, vr := range vrs {
		if !vr.IsDir() {
			continue
		}
		subDir := filepath.Join(vrsDir, vr.Name())
		mp := mps.Get("", subDir)
		switch mp {
		case types.MountPolicyIgnore:
			continue
		case types.MountPolicyLocal:
		case types.MountPolicyRemote, types.MountPolicyRemoteReadOnly:
			if _, err = dos.Stat(ctx, filepath.Join(vrsExportDir, vr.Name())); err == nil {
				break
			}
			if !hasVrsExportDir {
				if err = os.MkdirAll(vrsExportDir, 0o700); err != nil {
					return err
				}
				hasVrsExportDir = true
			}
			newName := filepath.Join(vrsExportDir, vr.Name())
			if err = dos.Symlink(ctx, subDir, newName); err != nil {
				return fmt.Errorf("can't symlink %s to %s: %v", subDir, newName, err)
			}
		}
		found := false
		sd := subDir
		for len(sd) > 1 && sd[0] == '/' {
			if _, found = ag.Mounts[sd]; found {
				break
			}
			sd = filepath.Dir(sd)
		}
		if !found {
			if ag.Mounts == nil {
				ag.Mounts = types.MountPolicies{subDir: mp}
			} else {
				ag.Mounts[subDir] = mp
			}
		}
	}
	return nil
}

// readMap parses a multi-line string into a map[string]string. Each line is assumed to be in the form "key=value",
// where the value is a JSON-encoded string.
func readMap(annData string) (map[string]string, error) {
	lines := strings.Split(annData, "\n")
	anns := make(map[string]string, len(lines))
	for _, line := range lines {
		if i := strings.IndexByte(line, '='); i > 0 {
			var st string
			if err := json.Unmarshal([]byte(line[i+1:]), &st); err != nil {
				return nil, err
			}
			anns[strings.TrimSpace(line[:i])] = st
		}
	}
	return anns, nil
}
