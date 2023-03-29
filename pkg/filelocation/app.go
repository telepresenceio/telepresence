package filelocation

import (
	"context"
	"path/filepath"
)

const appName = "telepresence"

// AppUserLogDir returns the directory to use for application-specific
// user-specific log files.
//
//   - On Darwin, it returns "$HOME/Library/Logs/telepresence".  Specified by:
//     https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/MacOSXDirectories/MacOSXDirectories.html
//
//   - On everything else, it returns "{{AppUserCacheDir}}/logs" (using the
//     appropriate path separator, if not "/").
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func AppUserLogDir(ctx context.Context) string {
	if logDir, ok := ctx.Value(logCtxKey{}).(string); ok && logDir != "" {
		return logDir
	}
	switch goos(ctx) {
	case "darwin":
		return filepath.Join(UserHomeDir(ctx), "Library", "Logs", appName)
	default: // Unix
		return filepath.Join(AppUserCacheDir(ctx), "logs")
	}
}

// AppUserCacheDir returns the directory to use for application-specific
// user-specific cache data.
//
// On all platforms, this returns "{{UserCacheDir}}/telepresence" (using the
// appropriate path separator, if not "/").
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func AppUserCacheDir(ctx context.Context) string {
	if cacheDir, ok := ctx.Value(cacheCtxKey{}).(string); ok && cacheDir != "" {
		return cacheDir
	}
	return filepath.Join(userCacheDir(ctx), appName)
}

// AppUserConfigDir returns the directory to use for application-specific
// user-specific configuration data.
//
// On all platforms, this returns "{{UserConfigDir}}/telepresence" (using the
// appropriate path separator, if not "/").
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func AppUserConfigDir(ctx context.Context) string {
	if configDir, ok := ctx.Value(configCtxKey{}).(string); ok && configDir != "" {
		return configDir
	}
	return filepath.Join(UserConfigDir(ctx), appName)
}

// AppSystemConfigDirs returns a list of directories to search for
// application-specific (but not user-specific) configuration data.
//
// On all platforms, this returns the list from SystemConfigDirs, with
// "/telepresence" appended to each directory (using the appropriate path
// separator, if not "/").
//
// If the location cannot be determined, then it will return an error.
func AppSystemConfigDirs(ctx context.Context) []string {
	if sysConfigDirs, ok := ctx.Value(sysConfigsCtxKey{}).([]string); ok && sysConfigDirs != nil {
		return sysConfigDirs
	}
	dirs := systemConfigDirs()
	ret := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		ret = append(ret, filepath.Join(dir, appName))
	}
	return ret
}
