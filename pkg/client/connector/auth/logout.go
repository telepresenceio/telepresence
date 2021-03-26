package auth

import (
	"context"
	"errors"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/auth/authdata"
)

var ErrNotLoggedIn = errors.New("not logged in")

func Logout(ctx context.Context) error {
	_, err := authdata.LoadTokenFromUserCache(ctx)
	if err != nil && os.IsNotExist(err) {
		return ErrNotLoggedIn
	}
	_ = authdata.DeleteTokenFromUserCache(ctx)
	_ = authdata.DeleteUserInfoFromUserCache(ctx)
	return nil
}
