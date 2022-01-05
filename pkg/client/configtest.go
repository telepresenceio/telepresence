package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/datawire/dtest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// SetDefaultConfig creates a config that has the registry set correctly.
// This ensures that the config on the machine of whatever is running the test,
// isn't used, which could cause conflict with the tests.
func SetDefaultConfig(ctx context.Context, configDir string) (context.Context, error) {
	registry := dtest.DockerRegistry(ctx)
	configYml := fmt.Sprintf(`
images:
  registry: %[1]s
  webhookRegistry: %[1]s
cloud:
  systemaHost: 127.0.0.1
`, registry)
	return SetConfig(ctx, configDir, configYml)
}

// SetConfig creates a config from the configYml provided and assigns it to a new context which
// is returned. Use this if you are testing components of the config.yml, otherwise you can use setDefaultConfig.
func SetConfig(ctx context.Context, configDir, configYml string) (context.Context, error) {
	config, err := os.Create(filepath.Join(configDir, "config.yml"))
	if err != nil {
		return ctx, err
	}

	_, err = config.WriteString(configYml)
	if err != nil {
		return ctx, err
	}
	config.Close()

	// Load env if it isn't loaded already
	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	if env := GetEnv(ctx); env == nil {
		env, err = LoadEnv(ctx)
		if err != nil {
			return ctx, err
		}
		ctx = WithEnv(ctx, env)
	}

	cfg, err := LoadConfig(ctx)
	if err != nil {
		return ctx, err
	}
	ctx = WithConfig(ctx, cfg)
	return ctx, nil
}
