package manager

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

type Env struct {
	User        string `env:"USER,default="`
	ServerHost  string `env:"SERVER_HOST,default="`
	ServerPort  string `env:"SERVER_PORT,default=8081"`
	SystemAHost string `env:"SYSTEMA_HOST,default=app.getambassador.io"`
	SystemAPort string `env:"SYSTEMA_PORT,default=443"`
	ClusterID   string `env:"CLUSTER_ID,default="`
}

func LoadEnv(ctx context.Context) (Env, error) {
	var env Env
	err := envconfig.Process(ctx, &env)
	return env, err
}
