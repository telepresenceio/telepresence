package cli

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/auth"
)

// Command returns the telepresence sub-command "auth"
func LoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "login",
		Args: cobra.NoArgs,

		Short: "Authenticate to Ambassador Cloud",
		Long:  "Authenticate to Ambassador Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			return auth.Login(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func LogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "logout",
		Args: cobra.NoArgs,

		Short: "Logout from Ambassador Cloud",
		Long:  "Logout from Ambassador Cloud",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return auth.Logout(cmd.Context())
		},
	}
}
