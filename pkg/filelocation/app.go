package filelocation

import (
	"context"
	"path/filepath"
)

const appName = "telepresence"

// AppUserLogDir returns the directory to use for application-specific
// user-specific log files.
//
//  - On Darwin, it returns "$HOME/Library/Logs/telepresnece".  Specified by:
//    https://developer.apple.com/library/archive/documentation/FileManagement/Conceptual/FileSystemProgrammingGuide/MacOSXDirectories/MacOSXDirectories.html
//
//  - On everything else, it returns "{{AppUserCacheDir}}/logs" (using the
//    appropriate path separator, if not "/").
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func AppUserLogDir(ctx context.Context) (string, error) {
	if untyped := ctx.Value(logCtxKey{}); untyped != nil {
		return untyped.(string), nil
	}
	switch goos(ctx) {
	case "darwin":
		home, err := UserHomeDir(ctx)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Logs", appName), nil
	default: // Unix
		appCacheDir, err := AppUserCacheDir(ctx)
		if err != nil {
			return "", err
		}
		return filepath.Join(appCacheDir, "logs"), nil
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
func AppUserCacheDir(ctx context.Context) (string, error) {
	userDir, err := userCacheDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(userDir, appName), nil
}

// AppUserConfigDir returns the directory to use for application-specific
// user-specific configuration data.
//
// On all platforms, this returns "{{UserConfigDir}}/telepresence" (using the
// appropriate path separator, if not "/").
//
// If the location cannot be determined (for example, $HOME is not defined),
// then it will return an error.
func AppUserConfigDir(ctx context.Context) (string, error) {
	userDir, err := UserConfigDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(userDir, appName), nil
}

// AppSystemConfigDirs returns a list of directories to search for
// application-specific (but not user-specific) configuration data.
//
// On all platforms, this returns the list from SystemConfigDirs, with
// "/telepresence" appended to each directory (using the appropriate path
// separator, if not "/").
//
// If the location cannot be determined, then it will return an error.
func AppSystemConfigDirs(ctx context.Context) ([]string, error) {
	dirs, err := systemConfigDirs(ctx)
	if err != nil {
		return nil, err
	}
	ret := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		ret = append(ret, filepath.Join(dir, appName))
	}
	return ret, nil
}
