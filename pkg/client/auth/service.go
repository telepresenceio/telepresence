package auth

import (
	"os"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

// Command returns the telepresence sub-command "auth"
func LoginCommand() *cobra.Command {
	authUrl := getEnvOrDefault("TELEPRESENCE_LOGIN_AUTH_URL", defaultOauthAuthUrl)
	tokenUrl := getEnvOrDefault("TELEPRESENCE_LOGIN_TOKEN_URL", defaultOauthTokenUrl)
	completionUrl := getEnvOrDefault("TELEPRESENCE_LOGIN_COMPLETION_URL", defaultCompletionUrl)
	clientId := getEnvOrDefault("TELEPRESENCE_LOGIN_CLIENT_ID", defaultOauthClientId)
	l := &LoginExecutor{
		Oauth2AuthUrl:  authUrl,
		Oauth2TokenUrl: tokenUrl,
		Oauth2ClientId: clientId,
		CompletionUrl:  completionUrl,
		SaveTokenFunc:  SaveTokenToUserCache,
		OpenURLFunc:    browser.OpenURL,
	}
	command := cobra.Command{
		Use:   "login",
		Short: "Authenticate to Ambassador Cloud",
		Long:  "Authenticate to Ambassador Cloud",
		RunE:  l.LoginFlow,
	}
	return &command
}

func getEnvOrDefault(varName, defaultValue string) string {
	value := os.Getenv(varName)
	if value == "" {
		value = defaultValue
	}
	return value
}
