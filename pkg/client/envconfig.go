package client

import (
	"context"
	"net/url"
	"os"
	"reflect"

	"github.com/datawire/envconfig"
)

type Env struct {
	// I'd like to set TELEPRESENCE_LOGIN_DOMAIN,default=auth.datawire.io, but
	// sethvargo/go-envconfig doesn't support filling in the default for our later references to
	// it in following settings, so we have to do the hack with maybeSetDefault below.  *sigh* I
	// guess I'm just spoiled by apro/cmd/amb-sidecar/types/internal/envconfig.
	LoginDomain        string   `env:"TELEPRESENCE_LOGIN_DOMAIN,        parser=nonempty-string"`
	LoginAuthURL       *url.URL `env:"TELEPRESENCE_LOGIN_AUTH_URL,      parser=absolute-URL,    default=https://${TELEPRESENCE_LOGIN_DOMAIN}/auth"`
	LoginTokenURL      *url.URL `env:"TELEPRESENCE_LOGIN_TOKEN_URL,     parser=absolute-URL,    default=https://${TELEPRESENCE_LOGIN_DOMAIN}/token"`
	LoginCompletionURL *url.URL `env:"TELEPRESENCE_LOGIN_COMPLETION_URL,parser=absolute-URL,    default=https://${TELEPRESENCE_LOGIN_DOMAIN}/completion"`
	UserInfoURL        *url.URL `env:"TELEPRESENCE_USER_INFO_URL,       parser=absolute-URL,    default=https://${TELEPRESENCE_LOGIN_DOMAIN}/api/userinfo"`
	ManagerNamespace   string   `env:"TELEPRESENCE_MANAGER_NAMESPACE,   parser=nonempty-string"`

	// This environment variable becomes the default for the images.registry and images.webhookRegistry
	Registry string `env:"TELEPRESENCE_REGISTRY,                        parser=nonempty-string,default=docker.io/datawire"`

	// This environment variable becomes the default for the images.agentImage and images.webhookAgentImage
	AgentImage string `env:"TELEPRESENCE_AGENT_IMAGE,                   parser=possibly-empty-string,default="`

	Shell string `env:"SHELL, parser=nonempty-string,default=/bin/bash"`

	TelepresenceUID int `env:"TELEPRESENCE_UID, parser=strconv.ParseInt, default=0"`
	TelepresenceGID int `env:"TELEPRESENCE_GID, parser=strconv.ParseInt, default=0"`

	// The address that the user daemon is listening to (unless it is started by the client and uses a named pipe or unix socket).
	UserDaemonAddress string `env:"TELEPRESENCE_USER_DAEMON_ADDRESS, parser=possibly-empty-string,default="`
	ScoutDisable      bool   `env:"SCOUT_DISABLE, parser=strconv.ParseBool, default=0"`

	lookupFunc func(key string) (string, bool)
}

func (env Env) Get(key string) string {
	switch key {
	case "TELEPRESENCE_LOGIN_DOMAIN":
		return env.LoginDomain
	case "TELEPRESENCE_LOGIN_AUTH_URL":
		return env.LoginAuthURL.String()
	case "TELEPRESENCE_LOGIN_TOKEN_URL":
		return env.LoginTokenURL.String()
	case "TELEPRESENCE_LOGIN_COMPLETION_URL":
		return env.LoginCompletionURL.String()
	case "TELEPRESENCE_USER_INFO_URL":
		return env.UserInfoURL.String()

	case "TELEPRESENCE_MANAGER_NAMESPACE":
		return env.ManagerNamespace

	default:
		if v, ok := env.lookupFunc(key); ok {
			return v
		}
		return ""
	}
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
	if _, ok := lookupFunc("TELEPRESENCE_LOGIN_DOMAIN"); !ok {
		olf := lookupFunc
		lookupFunc = func(key string) (string, bool) {
			if key == "TELEPRESENCE_LOGIN_DOMAIN" {
				if se, ok := olf("SYSTEMA_ENV"); ok && se == "staging" {
					// XXX : This is hacky bc we really should move TELEPRESENCE_LOGIN_DOMAIN
					// to the config.yml and get rid of that env var and all the related ones.
					// But I (donnyyung) am about to be on vacation for a week so don't want
					// to make such a huge change and then leave, so I will take care of
					// cleaning this up once I'm back.
					return "staging-auth.datawire.io", true
				}
				return "auth.datawire.io", true
			}
			return olf(key)
		}
	}

	var env Env
	parser, err := envconfig.GenerateParser(reflect.TypeOf(env), envconfig.DefaultFieldTypeHandlers())
	if err != nil {
		return nil, err
	}
	parser.ParseFromEnv(&env, lookupFunc)
	env.lookupFunc = lookupFunc
	return &env, nil
}
