package itest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type profileKey struct{}

type Profile string

const (
	DefaultProfile      Profile = "default"
	GkeAutopilotProfile Profile = "gke-autopilot"
)

func withProfile(ctx context.Context) context.Context {
	profile, ok := dos.LookupEnv(ctx, "TELEPRESENCE_TEST_PROFILE")
	if !ok {
		return context.WithValue(ctx, profileKey{}, DefaultProfile)
	}
	switch profile {
	case string(DefaultProfile):
	case string(GkeAutopilotProfile):
	default:
		panic("Invalid profile " + profile)
	}
	return context.WithValue(ctx, profileKey{}, Profile(profile))
}

func GetProfile(ctx context.Context) Profile {
	if profile, ok := ctx.Value(profileKey{}).(Profile); ok {
		return profile
	}
	return DefaultProfile
}

type tContextKey struct{}

func TestContext(t *testing.T, ossRoot, moduleRoot string) context.Context {
	ctx := context.Background()
	ctx = dlog.WithLogger(ctx, log.NewTestLogger(t, dlog.LogLevelDebug))
	ctx = client.WithEnv(ctx, &client.Env{})
	ctx = SetOSSRoot(ctx, ossRoot)
	ctx = SetModuleRoot(ctx, moduleRoot)
	ctx = withProfile(ctx)
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

type ossRootKey struct{}

func GetOSSRoot(ctx context.Context) string {
	if dir, ok := ctx.Value(ossRootKey{}).(string); ok {
		return dir
	}
	moduleRoot, err := os.Getwd()
	if err != nil {
		panic("failed to get current directory")
	}
	return filepath.Dir(moduleRoot)
}

// SetOSSRoot sets the OSS module root for the given context to dir.
func SetOSSRoot(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, ossRootKey{}, dir)
}

// WithOSSRoot set the working directory for the Command function to the
// OSS module root.
func WithOSSRoot(ctx context.Context) context.Context {
	return WithWorkingDir(ctx, GetOSSRoot(ctx))
}

type moduleRootKey struct{}

func GetModuleRoot(ctx context.Context) string {
	if dir, ok := ctx.Value(moduleRootKey{}).(string); ok {
		return dir
	}
	moduleRoot, err := os.Getwd()
	if err != nil {
		panic("failed to get current directory")
	}
	return filepath.Dir(moduleRoot)
}

// SetModuleRoot sets the module root for the given context to dir.
func SetModuleRoot(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, moduleRootKey{}, dir)
}

// WithModuleRoot set the working directory for the Command function to the
// module root.
func WithModuleRoot(ctx context.Context) context.Context {
	return WithWorkingDir(ctx, GetModuleRoot(ctx))
}

type dirContextKey struct{}

// WithWorkingDir determines the working directory for the Command function.
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
		maps.Merge(merged, prevEnv)
		maps.Merge(merged, env)
		env = merged
	}
	ctx = context.WithValue(ctx, envContextKey{}, env)
	evx, err := client.LoadEnvWith((&envCtxLookuper{ctx}).Lookup)
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

type useDockerContextkey struct{}

func WithUseDocker(ctx context.Context, use bool) context.Context {
	return context.WithValue(ctx, useDockerContextkey{}, use)
}

func UseDocker(ctx context.Context) bool {
	if use, ok := ctx.Value(useDockerContextkey{}).(bool); ok {
		return use
	}
	return false
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
