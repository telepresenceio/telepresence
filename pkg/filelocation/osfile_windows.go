package filelocation

import (
	"context"
	"os"
	"path/filepath"
)

const (
	appName       = "Telepresence"
	RootConfigDir = "C:\\ProgramData\\" + appName
	RootCacheDir  = RootConfigDir + "\\Cache"
)

func userHomeDir() string {
	if v := os.Getenv("USERPROFILE"); v != "" {
		return v
	}
	panic("%userprofile% is not defined")
}

func userCacheDir(ctx context.Context) string {
	dir := os.Getenv("LocalAppData")
	if dir == "" {
		dir = filepath.Join(UserHomeDir(ctx), "AppData", "Local")
	}
	return dir
}

func userConfigDir(ctx context.Context) string {
	dir := os.Getenv("AppData")
	if dir == "" {
		dir = filepath.Join(UserHomeDir(ctx), "AppData", "Roaming")
	}
	return dir
}

func appUserLogDir(ctx context.Context) string {
	return filepath.Join(AppUserCacheDir(ctx), "Logs")
}
