package manager

import (
	"context"

	"github.com/sethvargo/go-envconfig"
)

type Env struct {
	ClusterEnv

	User        string `env:"USER,default="`
	ServerHost  string `env:"SERVER_HOST,default="`
	ServerPort  string `env:"SERVER_PORT,default=8081"`
	SystemAHost string `env:"SYSTEMA_HOST,default=beta-app.datawire.io"`
	SystemAPort string `env:"SYSTEMA_PORT,default=443"`
}

func LoadEnv(ctx context.Context) (Env, error) {
	var env Env
	err := envconfig.Process(ctx, &env)
	env.ClusterEnv.AmbassadorClusterID = GetClusterID(ctx, env.ClusterEnv)
	return env, err
}
