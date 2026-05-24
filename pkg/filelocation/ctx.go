package filelocation

import (
	"context"
)

type homeCtxKey struct{}

// WithUserHomeDir spoofs the UserHomedir and all derived values for all functions in this package.
// This is useful for testing and should not be used in the normal code.
func WithUserHomeDir(ctx context.Context, home string) context.Context {
	return context.WithValue(ctx, homeCtxKey{}, home)
}

type logCtxKey struct{}

// WithAppUserLogDir spoofs the AppUserLogDir.  This is useful for testing or for when logging to a
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

type systemConfigCtxKey struct{}

// WithAppSystemConfigDir spoofs the AppSystemConfigDir. Useful in tests that
// need to point the loader at a writable temp dir rather than the real
// machine-wide location.
func WithAppSystemConfigDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, systemConfigCtxKey{}, dir)
}
