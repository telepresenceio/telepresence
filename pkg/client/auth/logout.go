package auth

import (
	"context"
	"errors"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/client/auth/authdata"
)

func Logout(ctx context.Context) error {
	_, err := authdata.LoadTokenFromUserCache(ctx)
	if err != nil && os.IsNotExist(err) {
		return errors.New("not logged in")
	}
	_ = authdata.DeleteTokenFromUserCache(ctx)
	_ = authdata.DeleteUserInfoFromUserCache(ctx)
	return nil
}
