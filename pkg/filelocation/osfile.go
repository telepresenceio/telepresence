package filelocation

import (
	"context"
)

// UserHomeDir returns the user's home directory path. It first checks for a context override
// value, otherwise it delegates to an OS-specific implementation that retrieves the home directory
// from environment variables ($HOME on Unix-like systems, %USERPROFILE% on Windows).
func UserHomeDir(ctx context.Context) string {
	if override, ok := ctx.Value(homeCtxKey{}).(string); ok {
		return override
	}
	return userHomeDir()
}

// UserCacheDir returns the default root directory to use for user-specific cached data.
// It delegates to an OS-specific implementation that returns:
//   - On Unix-like systems: $XDG_CACHE_HOME if set, otherwise $HOME/.cache
//   - On macOS: $HOME/Library/Caches
//   - On Windows: %LocalAppData% if set, otherwise %USERPROFILE%\AppData\Local
func UserCacheDir(ctx context.Context) string {
	return userCacheDir(ctx)
}

// UserConfigDir returns the default root directory to use for user-specific configuration data.
// It delegates to an OS-specific implementation that returns:
//   - On Unix-like systems: $XDG_CONFIG_HOME if set, otherwise $HOME/.config
//   - On macOS: $HOME/Library/Application Support
//   - On Windows: %AppData% if set, otherwise %USERPROFILE%\AppData\Roaming
func UserConfigDir(ctx context.Context) string {
	return userConfigDir(ctx)
}
