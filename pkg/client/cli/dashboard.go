package cli

import (
	"fmt"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/auth"
)

func dashboardCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Open the dashboard in a web page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := client.LoadEnv(cmd.Context())
			if err != nil {
				return err
			}

			// Login unless already logged in.
			if token, _ := auth.LoadTokenFromUserCache(); token == nil {
				l := &auth.LoginExecutor{
					Oauth2AuthUrl:  env.LoginAuthURL,
					Oauth2TokenUrl: env.LoginTokenURL,
					Oauth2ClientId: env.LoginClientID,
					CompletionUrl:  env.LoginCompletionURL,
					SaveTokenFunc:  auth.SaveTokenToUserCache,
					OpenURLFunc:    browser.OpenURL,
					Scout:          client.NewScout("cli"),
				}
				err = l.LoginFlow(cmd, args)
			} else {
				// The LoginFlow actually takes the user to the dashboard. Hence the else here.
				err = browser.OpenURL(fmt.Sprintf("https://%s/cloud/preview", env.SystemAHost))
			}
			return err
		}}
}
