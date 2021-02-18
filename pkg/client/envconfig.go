package client

import (
	"context"
	"os"

	"github.com/sethvargo/go-envconfig"
)

type Env struct {
	// I'd like to set TELEPRESENCE_LOGIN_DOMAIN,default=auth.datawire.io, but
	// sethvargo/go-envconfig doesn't support filling in the default for our later references to
	// it in following settings, so we have to do the hack with maybeSetDefault below.  *sigh* I
	// guess I'm just spoiled by apro/cmd/amb-sidecar/types/internal/envconfig.
	LoginDomain        string `env:"TELEPRESENCE_LOGIN_DOMAIN,required"`
	LoginAuthURL       string `env:"TELEPRESENCE_LOGIN_AUTH_URL,default=https://${TELEPRESENCE_LOGIN_DOMAIN}/auth"`
	LoginTokenURL      string `env:"TELEPRESENCE_LOGIN_TOKEN_URL,default=https://${TELEPRESENCE_LOGIN_DOMAIN}/token"`
	LoginCompletionURL string `env:"TELEPRESENCE_LOGIN_COMPLETION_URL,default=https://${TELEPRESENCE_LOGIN_DOMAIN}/completion"`
	LoginClientID      string `env:"TELEPRESENCE_LOGIN_CLIENT_ID,default=telepresence-cli"`
	UserInfoURL        string `env:"TELEPRESENCE_USER_INFO_URL,default=https://${TELEPRESENCE_LOGIN_DOMAIN}/api/userinfo"`

	Registry   string `env:"TELEPRESENCE_REGISTRY,default=docker.io/datawire"`
	AgentImage string `env:"TELEPRESENCE_AGENT_IMAGE,default="`

	SystemAHost string `env:"SYSTEMA_HOST,default=app.getambassador.io"`
	SystemAPort string `env:"SYSTEMA_PORT,default=443"`
}

func (env Env) Get(key string) string {
	switch key {
	case "TELEPRESENCE_LOGIN_DOMAIN":
		return env.LoginDomain
	case "TELEPRESENCE_LOGIN_AUTH_URL":
		return env.LoginAuthURL
	case "TELEPRESENCE_LOGIN_TOKEN_URL":
		return env.LoginTokenURL
	case "TELEPRESENCE_LOGIN_COMPLETION_URL":
		return env.LoginCompletionURL
	case "TELEPRESENCE_LOGIN_CLIENT_ID":
		return env.LoginClientID
	case "TELEPRESENCE_USER_INFO_URL":
		return env.UserInfoURL

	case "TELEPRESENCE_REGISTRY":
		return env.Registry
	case "TELEPRESENCE_AGENT_IMAGE":
		return env.AgentImage

	case "SYSTEMA_HOST":
		return env.SystemAHost
	case "SYSTEMA_PORT":
		return env.SystemAPort

	default:
		return os.Getenv(key)
	}
}

func maybeSetEnv(key, val string) {
	if os.Getenv(key) == "" {
		os.Setenv(key, val)
	}
}

func LoadEnv(ctx context.Context) (Env, error) {
	switch os.Getenv("SYSTEMA_ENV") {
	case "staging":
		maybeSetEnv("TELEPRESENCE_LOGIN_DOMAIN", "beta-auth.datawire.io")
		maybeSetEnv("SYSTEMA_HOST", "beta-app.datawire.io")
	default:
		maybeSetEnv("TELEPRESENCE_LOGIN_DOMAIN", "auth.datawire.io")
	}

	var env Env
	err := envconfig.Process(ctx, &env)
	return env, err
}
