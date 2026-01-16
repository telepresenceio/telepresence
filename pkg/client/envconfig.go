package client

import (
	"context"

	env2 "github.com/caarlos0/env/v11"
)

type Env struct {
	OSSpecificEnv
	ManagerNamespace string `env:"TELEPRESENCE_MANAGER_NAMESPACE" required:"true"`

	// This environment variable becomes the default for the images.registry and images.webhookRegistry
	Registry string `env:"TELEPRESENCE_REGISTRY"`

	// This environment variable becomes the default for the images.agentImage and images.webhookAgentImage
	AgentImage string `env:"TELEPRESENCE_AGENT_IMAGE"`

	// This environment variable becomes the default for the images.clientImage
	ClientImage string `env:"TELEPRESENCE_CLIENT_IMAGE"`

	// The address that the user daemon is listening to (unless it is started by the client and uses a named pipe or unix socket).
	UserDaemonAddress string `env:"TELEPRESENCE_USER_DAEMON_ADDRESS"`
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

func LoadEnv() (Env, error) {
	return env2.ParseAs[Env]()
}

func LoadEnvWith(environment map[string]string) (Env, error) {
	return env2.ParseAsWithOptions[Env](env2.Options{Environment: environment})
}
