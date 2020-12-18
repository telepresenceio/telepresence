package client

import (
	"os"
	"path/filepath"
)

const telepresenceCacheDir = "telepresence"

// CacheDir returns the full path to the directory "telepresence", parented by the directory returned
// by os.UserCacheDir(). The directory is created if it does not exist.
func CacheDir() (string, error) {
	userCacheDir, err := os.UserCacheDir()
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
