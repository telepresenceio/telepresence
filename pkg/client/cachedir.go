package client

import (
	"os"
	"path/filepath"
)

const telepresenceCacheDir = "telepresence"

var ucFunc = os.UserCacheDir

// UserCacheDir will call os.UserCacheDir and return the result unless that function has
// been overridden by SetUserCacheDirFunc
func UserCacheDir() (string, error) {
	return ucFunc()
}

// SetUserCacheDirFunc overrides the definition of UserCacheDir
//
// Intended for test purposes only.
func SetUserCacheDirFunc(ucf func() (string, error)) {
	ucFunc = ucf
}

// CacheDir returns the full path to the directory "telepresence", parented by the directory returned
// by UserCacheDir(). The directory is created if it does not exist.
func CacheDir() (string, error) {
	userCacheDir, err := UserCacheDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(userCacheDir, telepresenceCacheDir)
	err = os.MkdirAll(cacheDir, 0700)
	if err != nil {
		return "", err
	}
	return cacheDir, nil
}
