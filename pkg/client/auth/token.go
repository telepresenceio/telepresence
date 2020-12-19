package auth

import (
	"encoding/json"
	"io/ioutil"
	"path/filepath"

	"golang.org/x/oauth2"

	"github.com/datawire/telepresence2/pkg/client"
)

const (
	tokenFile = "tokens.json"
)

func SaveTokenToUserCache(token *oauth2.Token) error {
	cacheDir, err := client.CacheDir()
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
	cacheDir, err := client.CacheDir()
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
