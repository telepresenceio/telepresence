package cli

import (
	"fmt"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/auth/authdata"
)

func dashboardCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "dashboard",
		Args: cobra.NoArgs,

		Short: "Open the dashboard in a web page",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := client.LoadEnv(cmd.Context())
			if err != nil {
				return err
			}

			// Login unless already logged in.
			if token, _ := authdata.LoadTokenFromUserCache(cmd.Context()); token == nil {
				err = auth.Login(cmd.Context(), cmd.OutOrStdout())
			} else {
				// The LoginFlow actually takes the user to the dashboard. Hence the else here.
				err = browser.OpenURL(fmt.Sprintf("https://%s/cloud/preview", env.SystemAHost))
			}
			return err
		}}
}
