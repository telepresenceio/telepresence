package itest

import (
	"context"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

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
	if env := client.GetEnv(ctx); env == nil {
		env, err = client.LoadEnv()
		if err != nil {
			return ctx, err
		}
		ctx = client.WithEnv(ctx, env)
	}

	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return ctx, err
	}
	ctx = client.WithConfig(ctx, cfg)
	return ctx, nil
}
