package authdata

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const userInfoFile = "user-info.json"

type UserInfo = connector.UserInfo

// SaveUserInfoToUserCache saves the provided user info to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveUserInfoToUserCache(ctx context.Context, userInfo *UserInfo) error {
	return cache.SaveToUserCache(ctx, userInfo, userInfoFile)
}

// LoadUserInfoFromUserCache gets the user info from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadUserInfoFromUserCache(ctx context.Context) (*UserInfo, error) {
	var userInfo UserInfo
	err := cache.LoadFromUserCache(ctx, &userInfo, userInfoFile)
	if err != nil {
		return nil, err
	}
	return &userInfo, nil
}

// DeleteUserInfoFromUserCache removes user info cache if existing or returns an error. An attempt
// to remove a non existing cache is a no-op and the function returns nil.
func DeleteUserInfoFromUserCache(ctx context.Context) error {
	return cache.DeleteFromUserCache(ctx, userInfoFile)
}
