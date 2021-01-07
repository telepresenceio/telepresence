package client

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

type Env struct {
	LoginAuthURL       string `env:"TELEPRESENCE_LOGIN_AUTH_URL,default=https://auth.datawire.io/auth"`
	LoginTokenURL      string `env:"TELEPRESENCE_LOGIN_TOKEN_URL,default=https://auth.datawire.io/token"`
	LoginCompletionURL string `env:"TELEPRESENCE_LOGIN_COMPLETION_URL,default=https://auth.datawire.io/completion"`
	LoginClientID      string `env:"TELEPRESENCE_LOGIN_CLIENT_ID,default=telepresence-cli"`

	Registry string `env:"TELEPRESENCE_REGISTRY,default=docker.io/datawire"`

	SystemAHost string `env:"SYSTEMA_HOST,default="`
	SystemAPort string `env:"SYSTEMA_PORT,default="`
}

func LoadEnv(ctx context.Context) (Env, error) {
	var env Env
	err := envconfig.Process(ctx, &env)
	return env, err
}
