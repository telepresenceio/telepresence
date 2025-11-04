package filelocation

import (
	"context"
	"os"
	"path/filepath"
)

func userHomeDir() string {
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	panic("$HOME is not defined")
}

func userCacheDir(ctx context.Context) string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		dir = filepath.Join(UserHomeDir(ctx), ".cache")
	}
	return dir
}

func userConfigDir(ctx context.Context) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(UserHomeDir(ctx), ".config")
	}
	return dir
}

func appUserLogDir(ctx context.Context) string {
	return filepath.Join(AppUserCacheDir(ctx), "logs")
}
