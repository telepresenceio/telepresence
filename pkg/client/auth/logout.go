package auth

import (
	"context"
	"errors"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

func Logout(ctx context.Context) error {
	_, err := cache.LoadTokenFromUserCache(ctx)
	if err != nil && os.IsNotExist(err) {
		return errors.New("not logged in")
	}
	_ = cache.DeleteTokenFromUserCache(ctx)
	_ = cache.DeleteUserInfoFromUserCache(ctx)
	return nil
}
