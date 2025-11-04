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
	return appUserLogDir(ctx)
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
	return filepath.Join(UserCacheDir(ctx), appName)
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
