package cli

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func LoginCommand() *cobra.Command {
	var args struct {
		apikey string
	}
	cmd := &cobra.Command{
		Use:  "login",
		Args: cobra.NoArgs,

		Short: "Authenticate to Ambassador Cloud",
		Long:  "Authenticate to Ambassador Cloud",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cliutil.EnsureLoggedIn(cmd.Context(), args.apikey)
			return err
		},
	}
	cmd.Flags().StringVar(&args.apikey, "apikey", "",
		"Static API key to use instead of performing an interactive login")
	return cmd
}

func LogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "logout",
		Args: cobra.NoArgs,

		Short: "Logout from Ambassador Cloud",
		Long:  "Logout from Ambassador Cloud",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cliutil.Logout(cmd.Context())
		},
	}
}
