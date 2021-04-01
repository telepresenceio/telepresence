package cliutil

import (
	"context"
	"errors"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

// HasLoggedIn returns true if either the user has an active login session or an expired login
// session, and returns false if either the user has never logged in or has explicitly logged out.
func HasLoggedIn(ctx context.Context) bool {
	token, _ := cache.LoadTokenFromUserCache(ctx)
	return token != nil
}

func GetCloudAccessToken(ctx context.Context) (string, error) {
	tokenData, err := cache.LoadTokenFromUserCache(ctx)
	if err != nil {
		return "", err
	}
	if !tokenData.Valid() {
		return "", errors.New("login expired")
	}
	return tokenData.AccessToken, nil
}
