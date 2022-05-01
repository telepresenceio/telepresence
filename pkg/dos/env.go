package dos

import (
	"context"
	"os"
	"sort"
)

// Env is an abstraction of the environment related functions with the same name in the os package.
type Env interface {
	Environ() []string
	ExpandEnv(string) string
	Getenv(string) string
	Setenv(string, string) error
	Lookup(string) (string, bool)
}

type MapEnv map[string]string

func (e MapEnv) Environ() []string {
	ks := make([]string, len(e))
	i := 0
	for k := range e {
		ks[i] = k
		i++
	}
	sort.Strings(ks)
	for i, k := range ks {
		ks[i] = k + "=" + e[k]
	}
	return ks
}

func (e MapEnv) ExpandEnv(s string) string {
	return os.Expand(s, e.Getenv)
}

func (e MapEnv) Getenv(key string) string {
	return e[key]
}

func (e MapEnv) Setenv(key, value string) error {
	e[key] = value
	return nil
}

func (e MapEnv) Lookup(key string) (string, bool) {
	s, ok := e[key]
	return s, ok
}

type envKey struct{}

func WithEnv(ctx context.Context, env Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

// EnvAPI returns the Env that has been registered with the given context, or
// the instance that delegates to the env functions in the os package
func EnvAPI(ctx context.Context) Env {
	if e, ok := ctx.Value(envKey{}).(Env); ok {
		return e
	}
	return osEnv{}
}

func Environ(ctx context.Context) []string {
	return EnvAPI(ctx).Environ()
}

func ExpandEnv(ctx context.Context, s string) string {
	return EnvAPI(ctx).ExpandEnv(s)
}

func Getenv(ctx context.Context, key string) string {
	return EnvAPI(ctx).Getenv(key)
}

func Setenv(ctx context.Context, key, value string) error {
	return EnvAPI(ctx).Setenv(key, value)
}

func LookupEnv(ctx context.Context, key string) (string, bool) {
	return EnvAPI(ctx).Lookup(key)
}

type osEnv struct{}

func (osEnv) Environ() []string {
	return os.Environ()
}

func (osEnv) ExpandEnv(s string) string {
	return os.ExpandEnv(s)
}

func (osEnv) Getenv(s string) string {
	return os.Getenv(s)
}

func (osEnv) Setenv(key, value string) error {
	return os.Setenv(key, value)
}

func (osEnv) Lookup(s string) (string, bool) {
	return os.LookupEnv(s)
}
