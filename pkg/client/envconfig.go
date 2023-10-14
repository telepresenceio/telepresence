package client

import (
	"context"
	"os"
	"reflect"

	"github.com/datawire/envconfig"
)

type Env struct {
	OSSpecificEnv
	ManagerNamespace string `env:"TELEPRESENCE_MANAGER_NAMESPACE,   parser=nonempty-string"`

	// This environment variable becomes the default for the images.registry and images.webhookRegistry
	Registry string `env:"TELEPRESENCE_REGISTRY,                        parser=possibly-empty-string,default="`

	// This environment variable becomes the default for the images.agentImage and images.webhookAgentImage
	AgentImage string `env:"TELEPRESENCE_AGENT_IMAGE,                   parser=possibly-empty-string,default="`

	// This environment variable becomes the default for the images.clientImage
	ClientImage string `env:"TELEPRESENCE_CLIENT_IMAGE,                   parser=possibly-empty-string,default="`

	// The address that the user daemon is listening to (unless it is started by the client and uses a named pipe or unix socket).
	UserDaemonAddress string `env:"TELEPRESENCE_USER_DAEMON_ADDRESS, parser=possibly-empty-string,default="`
	ScoutDisable      bool   `env:"SCOUT_DISABLE, parser=strconv.ParseBool, default=0"`
}

type envKey struct{}

// WithEnv returns a context with the given Env.
func WithEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func GetEnv(ctx context.Context) *Env {
	env, ok := ctx.Value(envKey{}).(*Env)
	if !ok {
		return nil
	}
	return env
}

func LoadEnv() (*Env, error) {
	return LoadEnvWith(os.LookupEnv)
}

func LoadEnvWith(lookupFunc func(key string) (string, bool)) (*Env, error) {
	env, err := LoadEnvWithInto(lookupFunc, Env{})
	if err != nil {
		return nil, err
	}
	return env.(*Env), nil
}

func LoadEnvWithInto(lookupFunc func(key string) (string, bool), env any) (any, error) {
	et := reflect.ValueOf(env)
	parser, err := envconfig.GenerateParser(et.Type(), envconfig.DefaultFieldTypeHandlers())
	if err != nil {
		return nil, err
	}
	ptr := reflect.New(et.Type())
	ptr.Elem().Set(et)
	parser.ParseFromEnv(ptr.Interface(), lookupFunc)
	return ptr.Interface(), nil
}
