package auth

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

const (
	telepresenceCacheDir = "telepresence"
	tokenFile            = "tokens.json"
)

func SaveTokenToUserCache(token *oauth2.Token) error {
	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}
	tokenJson, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(cacheDir, tokenFile), tokenJson, 0600)
}

func LoadTokenFromUserCache() (*oauth2.Token, error) {
	cacheDir, err := getCacheDir()
	if err != nil {
		return nil, err
	}
	tokenJson, err := ioutil.ReadFile(filepath.Join(cacheDir, tokenFile))
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(tokenJson, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func getCacheDir() (string, error) {
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
