package auth

import (
	"errors"
	"os"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/cache"
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
				env.UserInfoUrl,
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
			_ = cache.DeleteUserInfoFromUserCache()
			err := cache.DeleteTokenFromUserCache()
			if err != nil {
				if os.IsNotExist(err) {
					err = errors.New("not logged in")
				}
				return err
			}
			return nil
		},
	}
}
