package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
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

// Keys that aren't useful when running on the local machine
var skipKeys = map[string]bool{
	"HOME":     true,
	"PATH":     true,
	"HOSTNAME": true,
}

func LoadConfig(ctx context.Context) (Config, error) {
	cf, err := dos.Open(ctx, filepath.Join(agentconfig.ConfigMountPoint, agentconfig.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("unable to open agent ConfigMap: %w", err)
	}
	defer cf.Close()

	c := config{}
	if err = yaml.NewDecoder(cf).Decode(&c.Sidecar); err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	c.podIP = dos.Getenv(ctx, "_TEL_AGENT_POD_IP")
	for _, cn := range c.Containers {
		if err := addSecretsMounts(ctx, cn); err != nil {
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

// addSecretsMounts adds any token-rotating system secrets directories if they exist
// e.g. /var/run/secrets/kubernetes.io or /var/run/secrets/eks.amazonaws.com
// to the TELEPRESENCE_MOUNTS environment variable
func addSecretsMounts(ctx context.Context, ag *agentconfig.Container) error {
	// This will attempt to handle all the secrets dirs, but will return the first error we encountered.
	secretsDir, err := dos.Open(ctx, "/var/run/secrets")
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return err
	}
	fileInfo, err := secretsDir.ReadDir(-1)
	if err != nil {
		return err
	}
	secretsDir.Close()

	mm := make(map[string]struct{})
	for _, m := range ag.Mounts {
		mm[m] = struct{}{}
	}

	for _, file := range fileInfo {
		// Directories found in /var/run/secrets get a symlink in appmounts
		if !file.IsDir() {
			continue
		}
		dirPath := filepath.Join("/var/run/secrets/", file.Name())
		dlog.Debugf(ctx, "checking agent secrets mount path: %s", dirPath)
		stat, err := dos.Stat(ctx, dirPath)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			continue
		}
		if _, ok := mm[dirPath]; ok {
			continue
		}

		appMountsPath := filepath.Join(ag.MountPoint, dirPath)
		dlog.Debugf(ctx, "checking appmounts directory: %s", dirPath)
		// Make sure the path doesn't already exist
		_, err = dos.Stat(ctx, appMountsPath)
		if err == nil {
			return fmt.Errorf("appmounts '%s' already exists", appMountsPath)
		}
		dlog.Debugf(ctx, "create appmounts directory: %s", appMountsPath)
		// Add a link to the kubernetes.io directory under {{.AppMounts}}/var/run/secrets
		err = dos.MkdirAll(ctx, filepath.Dir(appMountsPath), 0700)
		if err != nil {
			return err
		}
		dlog.Debugf(ctx, "create appmounts symlink: %s %s", dirPath, appMountsPath)
		err = dos.Symlink(ctx, dirPath, appMountsPath)
		if err != nil {
			return err
		}
		dlog.Infof(ctx, "new agent secrets mount path: %s", dirPath)
		ag.Mounts = append(ag.Mounts, dirPath)
		mm[dirPath] = struct{}{}
	}
	return nil
}
