package filelocation

import (
	"context"
	"runtime"
)

type goosCtxKey struct{}

// WithGOOS spoofs the runtime.GOOS for all functions in this package.  This is useful for testing,
// and should not be used in the normal code.
func WithGOOS(ctx context.Context, goos string) context.Context {
	return context.WithValue(ctx, goosCtxKey{}, goos)
}

// goos returns the runtime.GOOS, or the spoofed value from WithGOOS.  You should therefore use it
// instead of runtime.GOOS.
func goos(ctx context.Context) string {
	if untyped := ctx.Value(goosCtxKey{}); untyped != nil {
		return untyped.(string)
	}
	return runtime.GOOS
}

type homeCtxKey struct{}

// WithUserHomeDir spoofs the UserHomedir and all derived values for all functions in this package.
// This is useful for testing, and should not be used in the normal code.
func WithUserHomeDir(ctx context.Context, home string) context.Context {
	return context.WithValue(ctx, homeCtxKey{}, home)
}

type logCtxKey struct{}

// WithAppUserLogDir spoofs the AppUserLogDir.  This is useful for testing, or for when logging to a
// normal user's logs as root.
func WithAppUserLogDir(ctx context.Context, logdir string) context.Context {
	return context.WithValue(ctx, logCtxKey{}, logdir)
}

type configCtxKey struct{}

// WithAppUserConfigDir spoofs the AppUserConfigDir.  This is useful for testing.
func WithAppUserConfigDir(ctx context.Context, configDir string) context.Context {
	return context.WithValue(ctx, configCtxKey{}, configDir)
}

type cacheCtxKey struct{}

// WithAppUserCacheDir spoofs the AppUserCacheDir.  This is useful for testing.
func WithAppUserCacheDir(ctx context.Context, cacheDir string) context.Context {
	return context.WithValue(ctx, cacheCtxKey{}, cacheDir)
}

type sysConfigsCtxKey struct{}

// WithAppSystemConfigDirs spoofs the AppSystemConfigDirs.  This is useful for testing.
func WithAppSystemConfigDirs(ctx context.Context, configDirs []string) context.Context {
	return context.WithValue(ctx, sysConfigsCtxKey{}, configDirs)
}
