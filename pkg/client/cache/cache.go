package cache

import (
	"encoding/json"
	"io/ioutil"
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

func saveToUserCache(object interface{}, file string) error {
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}
	jsonContent, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(cacheDir, file), jsonContent, 0600)
}

func loadFromUserCache(dest interface{}, file string) error {
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}
	jsonContent, err := ioutil.ReadFile(filepath.Join(cacheDir, file))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonContent, &dest); err != nil {
		return err
	}
	return nil
}

func deleteFromUserCache(file string) error {
	cacheDir, err := CacheDir()
	if err != nil {
		return err
	}
	cacheFile := filepath.Join(cacheDir, file)
	if _, err = os.Stat(cacheFile); err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return err
	}
	return os.Remove(cacheFile)
}
