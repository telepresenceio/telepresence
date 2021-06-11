package managerutil

import (
	"context"
	"strings"

	"github.com/sethvargo/go-envconfig"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type Env struct {
	User        string `env:"USER,default="`
	ServerHost  string `env:"SERVER_HOST,default="`
	ServerPort  string `env:"SERVER_PORT,default=8081"`
	SystemAHost string `env:"SYSTEMA_HOST,default=app.getambassador.io"`
	SystemAPort string `env:"SYSTEMA_PORT,default=443"`
	ClusterID   string `env:"CLUSTER_ID,default="`

	ManagerNamespace string `env:"MANAGER_NAMESPACE,default="`
	AgentRegistry    string `env:"TELEPRESENCE_REGISTRY,default=docker.io/datawire"`
	AgentImage       string `env:"TELEPRESENCE_AGENT_IMAGE,default="`
	AgentPort        int32  `env:"TELEPRESENCE_AGENT_PORT,default=9900"`
}

type envKey struct{}

func LoadEnv(ctx context.Context) (context.Context, error) {
	var env Env
	if err := envconfig.Process(ctx, &env); err != nil {
		return ctx, err
	}
	if env.AgentImage == "" {
		env.AgentImage = env.AgentRegistry + "/tel2:" + strings.TrimPrefix(version.Version, "v")
	}
	return WithEnv(ctx, &env), nil
}

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
