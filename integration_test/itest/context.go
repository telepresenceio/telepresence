package itest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type tContextKey struct{}

func TestContext(t *testing.T) context.Context {
	ctx := context.Background()
	ctx = dlog.WithLogger(ctx, log.NewTestLogger(t, dlog.LogLevelDebug))
	ctx = client.WithEnv(ctx, &client.Env{})
	return withT(ctx, t)
}

func withT(ctx context.Context, t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(dlog.WithLogger(context.WithValue(ctx, tContextKey{}, t), log.NewTestLogger(t, dlog.LogLevelDebug)))
	t.Cleanup(cancel)
	return ctx
}

func getT(ctx context.Context) *testing.T {
	if t, ok := ctx.Value(tContextKey{}).(*testing.T); ok {
		return t
	}
	panic("No *testing.T in context")
}

// WithModuleRoot set the working directory for the Command function to the
// module root.
func WithModuleRoot(ctx context.Context) context.Context {
	t := getT(ctx)
	moduleRoot, err := filepath.Abs("..")
	require.NoError(t, err, "failed to resolver module root")
	return WithWorkingDir(ctx, moduleRoot)
}

type dirContextKey struct{}

// WithWorkingDir determines the working directory for the Command function
func WithWorkingDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, dirContextKey{}, dir)
}

func GetWorkingDir(ctx context.Context) string {
	if dir, ok := ctx.Value(dirContextKey{}).(string); ok {
		return dir
	}
	dir, err := os.Getwd()
	require.NoError(getT(ctx), err, "failed to get working directory")
	return dir
}

type envContextKey struct{}

type envCtxLookuper struct {
	context.Context
}

func (e envCtxLookuper) Lookup(key string) (string, bool) {
	return LookupEnv(e, key)
}

// WithEnv adds environment variables to be used by the Command function.
func WithEnv(ctx context.Context, env map[string]string) context.Context {
	if prevEnv := getEnv(ctx); prevEnv != nil {
		merged := make(map[string]string, len(prevEnv)+len(env))
		for k, v := range prevEnv {
			merged[k] = v
		}
		for k, v := range env {
			merged[k] = v
		}
		env = merged
	}
	ctx = context.WithValue(ctx, envContextKey{}, env)
	evx, err := client.LoadEnvWith(ctx, &envCtxLookuper{ctx})
	if err != nil {
		getT(ctx).Fatal(err)
	}
	return client.WithEnv(ctx, evx)
}

type userContextkey struct{}

func WithUser(ctx context.Context, clusterUser string) context.Context {
	return context.WithValue(ctx, userContextkey{}, clusterUser)
}

func GetUser(ctx context.Context) string {
	if user, ok := ctx.Value(userContextkey{}).(string); ok {
		return user
	}
	return "default"
}

func LookupEnv(ctx context.Context, key string) (value string, ok bool) {
	if value, ok = getEnv(ctx)[key]; !ok {
		value, ok = GetGlobalHarness(ctx).GlobalEnv()[key]
	}
	return
}

func getEnv(ctx context.Context) map[string]string {
	if env, ok := ctx.Value(envContextKey{}).(map[string]string); ok {
		return env
	}
	return nil
}

type globalHarnessContextKey struct{}

func withGlobalHarness(ctx context.Context, h *cluster) context.Context {
	return context.WithValue(ctx, globalHarnessContextKey{}, h)
}

func GetGlobalHarness(ctx context.Context) *cluster {
	if h, ok := ctx.Value(globalHarnessContextKey{}).(*cluster); ok {
		return h
	}
	panic("No globalHarness in context")
}
