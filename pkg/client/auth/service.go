package auth

import (
	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
)

// Command returns the telepresence sub-command "auth"
func LoginCommand() *cobra.Command {
	command := cobra.Command{
		Use:   "login",
		Short: "Authenticate to Ambassador Cloud",
		Long:  "Authenticate to Ambassador Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := client.LoadEnv(cmd.Context())
			if err != nil {
				return err
			}
			l := &LoginExecutor{
				Oauth2AuthUrl:  env.LoginAuthURL,
				Oauth2TokenUrl: env.LoginTokenURL,
				Oauth2ClientId: env.LoginClientID,
				CompletionUrl:  env.LoginCompletionURL,
				SaveTokenFunc:  SaveTokenToUserCache,
				OpenURLFunc:    browser.OpenURL,
				Scout:          client.NewScout("cli"),
			}
			return l.LoginFlow(cmd, args)
		},
	}
	return &command
}
