package filelocation

import (
	"context"
	"os"
	"path/filepath"
)

const (
	appName       = "telepresence"
	RootCacheDir  = "/Library/Caches/" + appName
	RootConfigDir = "/Library/Application Support/" + appName
)

func userHomeDir() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	panic("$HOME is not defined")
}

func userCacheDir(ctx context.Context) string {
	return filepath.Join(UserHomeDir(ctx), "Library", "Caches")
}

func userConfigDir(ctx context.Context) string {
	return filepath.Join(UserHomeDir(ctx), "Library", "Application Support")
}

func appUserLogDir(ctx context.Context) string {
	return filepath.Join(UserHomeDir(ctx), "Library", "Logs", appName)
}
