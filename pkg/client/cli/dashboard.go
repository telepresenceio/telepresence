package cli

import (
	"fmt"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
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

			// Ensure we're logged in
			resultCode, err := cliutil.EnsureLoggedIn(cmd.Context())
			if err != nil {
				return err
			}

			if resultCode == connector.LoginResult_OLD_LOGIN_REUSED {
				// The LoginFlow takes the user to the dashboard, so we only need to
				// explicitly take the user to the dashboard if they were already
				// logged in.
				if err := browser.OpenURL(fmt.Sprintf("https://%s/cloud/preview", env.SystemAHost)); err != nil {
					return err
				}
			}

			return nil
		}}
}
