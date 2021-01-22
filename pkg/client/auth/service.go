package auth

import (
	"errors"
	"os"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
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
			l := NewLoginExecutor(
				env.LoginAuthURL,
				env.LoginTokenURL,
				env.LoginClientID,
				env.LoginCompletionURL,
				env.UserInfoURL,
				cache.SaveTokenToUserCache,
				cache.SaveUserInfoToUserCache,
				browser.OpenURL,
				client.NewScout("cli"),
			)
			return l.LoginFlow(cmd, args)
		},
	}
	return &command
}

func LogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout from Ambassador Cloud",
		Long:  "Logout from Ambassador Cloud",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cache.LoadTokenFromUserCache()
			if err != nil && os.IsNotExist(err) {
				return errors.New("not logged in")
			}
			_ = cache.DeleteTokenFromUserCache()
			_ = cache.DeleteUserInfoFromUserCache()
			return nil
		},
	}
}
