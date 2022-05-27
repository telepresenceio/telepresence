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
	UserInfoURL        string `env:"TELEPRESENCE_USER_INFO_URL,default=https://${TELEPRESENCE_LOGIN_DOMAIN}/api/userinfo"`

	ManagerNamespace string `env:"TELEPRESENCE_MANAGER_NAMESPACE,default=ambassador"`

	// This environment variable becomes the default for the images.registry and images.webhookRegistry
	Registry string `env:"TELEPRESENCE_REGISTRY,default=docker.io/datawire"`

	// This environment variable becomes the default for the images.agentImage and images.webhookAgentImage
	AgentImage string `env:"TELEPRESENCE_AGENT_IMAGE,default="`

	lookuper envconfig.Lookuper
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
	case "TELEPRESENCE_USER_INFO_URL":
		return env.UserInfoURL

	case "TELEPRESENCE_MANAGER_NAMESPACE":
		return env.ManagerNamespace

	default:
		if v, ok := env.lookuper.Lookup(key); ok {
			return v
		}
		return ""
	}
}

type envKey struct{}

// WithEnv returns a context with the given Env
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

func LoadEnv(ctx context.Context) (*Env, error) {
	return LoadEnvWith(ctx, envconfig.OsLookuper())
}

func LoadEnvWith(ctx context.Context, lookuper envconfig.Lookuper) (*Env, error) {
	if _, ok := lookuper.Lookup("TELEPRESENCE_LOGIN_DOMAIN"); !ok {
		loginDomain := "auth.datawire.io"
		if se, ok := lookuper.Lookup("SYSTEMA_ENV"); ok && se == "staging" {
			// XXX : This is hacky bc we really should move TELEPRESENCE_LOGIN_DOMAIN
			// to the config.yml and get rid of that env var and all the related ones.
			// But I (donnyyung) am about to be on vacation for a week so don't want
			// to make such a huge change and then leave, so I will take care of
			// cleaning this up once I'm back.
			loginDomain = "staging-auth.datawire.io"
		}
		os.Setenv("TELEPRESENCE_LOGIN_DOMAIN", loginDomain)
	}

	var env Env
	if err := envconfig.ProcessWith(ctx, &env, lookuper); err != nil {
		return nil, err
	}
	env.lookuper = lookuper
	return &env, nil
}
