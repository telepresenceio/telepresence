package cache

import (
	"context"
)

const userInfoFile = "user-info.json"

type UserInfo struct {
	Id               string `json:"id"`
	Name             string `json:"name"`
	AvatarUrl        string `json:"avatarUrl"`
	AccountId        string `json:"accountId"`
	AccountName      string `json:"accountName"`
	AccountAvatarUrl string `json:"accountAvatarUrl"`
}

// SaveUserInfoToUserCache saves the provided user info to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveUserInfoToUserCache(ctx context.Context, userInfo *UserInfo) error {
	return SaveToUserCache(ctx, userInfo, userInfoFile)
}

// LoadUserInfoFromUserCache gets the user info from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadUserInfoFromUserCache(ctx context.Context) (*UserInfo, error) {
	var userInfo UserInfo
	err := LoadFromUserCache(ctx, &userInfo, userInfoFile)
	if err != nil {
		return nil, err
	}
	return &userInfo, nil
}

// DeleteUserInfoFromUserCache removes user info cache if existing or returns an error. An attempt
// to remove a non existing cache is a no-op and the function returns nil.
func DeleteUserInfoFromUserCache(ctx context.Context) error {
	return DeleteFromUserCache(ctx, userInfoFile)
}
