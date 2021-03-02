package cliutil

import (
	"context"
	"os"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/auth/authdata"
)

// EnsureLoggedIn ensures that the user is logged in to Ambassador Cloud.  An error is returned if
// login fails.  The result code will indicate if this is a new login or if it resued an existing
// login.
func EnsureLoggedIn(ctx context.Context) (connector.LoginResult_Code, error) {
	return auth.EnsureLoggedIn(ctx, os.Stdout)
}

// Logout logs out of Ambassador Cloud.  Returns auth.ErrNotLoggedIn if not logged in.
func Logout(ctx context.Context) error {
	return auth.Logout(ctx)
}

// EnsureLoggedOut ensures that the user is logged out of Ambassador Cloud.  Returns nil if not
// logged in.
func EnsureLoggedOut(ctx context.Context) error {
	err := Logout(ctx)
	if err == auth.ErrNotLoggedIn {
		err = nil
	}
	return err
}

// HasLoggedIn returns true if either the user has an active login session or an expired login
// session, and returns false if either the user has never logged in or has explicitly logged out.
func HasLoggedIn(ctx context.Context) bool {
	token, _ := authdata.LoadTokenFromUserCache(ctx)
	return token != nil
}

func GetCloudToken(ctx context.Context) (string, error) {
	tokenData, err := authdata.LoadTokenFromUserCache(ctx)
	if err != nil {
		return "", err
	}
	return tokenData.AccessToken, nil
}
