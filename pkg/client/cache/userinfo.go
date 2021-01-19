package cache

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
func SaveUserInfoToUserCache(userInfo *UserInfo) error {
	return saveToUserCache(userInfo, userInfoFile)
}

// LoadUserInfoFromUserCache gets the user info from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadUserInfoFromUserCache() (*UserInfo, error) {
	var userInfo UserInfo
	err := loadFromUserCache(&userInfo, userInfoFile)
	if err != nil {
		return nil, err
	}
	return &userInfo, nil
}

// DeleteUserInfoFromUserCache removes user info cache if existing or returns an error. An attempt
// to remove a non existing cache is a no-op and the function returns nil.
func DeleteUserInfoFromUserCache() error {
	return deleteFromUserCache(userInfoFile)
}
