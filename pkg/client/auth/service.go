package auth

import (
	"errors"
	"os"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

// Command returns the telepresence sub-command "auth"
func LoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "login",
		Args: cobra.NoArgs,

		Short: "Authenticate to Ambassador Cloud",
		Long:  "Authenticate to Ambassador Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := client.LoadEnv(cmd.Context())
			if err != nil {
				return err
			}
			l := NewLoginExecutor(
				env.LoginAuthURL,
				env.LoginTokenURL,
				env.LoginClientID,
				env.LoginCompletionURL,
				env.UserInfoURL,
				cache.SaveTokenToUserCache,
				cache.SaveUserInfoToUserCache,
				browser.OpenURL,
				client.NewScout(cmd.Context(), "cli"),
			)
			return l.LoginFlow(cmd)
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
			_, err := cache.LoadTokenFromUserCache(cmd.Context())
			if err != nil && os.IsNotExist(err) {
				return errors.New("not logged in")
			}
			_ = cache.DeleteTokenFromUserCache(cmd.Context())
			_ = cache.DeleteUserInfoFromUserCache(cmd.Context())
			return nil
		},
	}
}
