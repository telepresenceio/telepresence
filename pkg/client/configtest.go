package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/datawire/ambassador/pkg/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// ResetConfig updates configOnce with a new sync.Once. This is currently only used
// for tests.
func ResetConfig(c context.Context) {
	configOnce = new(sync.Once)
}

// setDefaultConfig creates a config that has the registry set correctly.
// This ensures that the config on the machine of whatever is running the test,
// isn't used, which could cause conflict with the tests.
func SetDefaultConfig(ctx context.Context, configDir string) (context.Context, error) {
	registry := dtest.DockerRegistry(ctx)
	configYml := fmt.Sprintf("images:\n  registry: %s\n  webhookRegistry: %s\n", registry, registry)
	return SetConfig(ctx, configDir, configYml)
}

// SetConfig clears the config and creates one from the configYml provided. Use this
// if you are testing components of the config.yml, otherwise you can use setDefaultConfig.
func SetConfig(ctx context.Context, configDir, configYml string) (context.Context, error) {
	ResetConfig(ctx)
	config, err := os.Create(filepath.Join(configDir, "config.yml"))
	if err != nil {
		return ctx, err
	}

	_, err = config.WriteString(configYml)
	if err != nil {
		return ctx, err
	}
	config.Close()

	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	return ctx, nil
}
