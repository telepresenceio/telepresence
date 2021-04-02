package authdata

import (
	"context"

	"golang.org/x/oauth2"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const (
	tokenFile = "tokens.json"
)

// SaveTokenToUserCache saves the provided token to user cache and returns an error if something
// goes wrong while marshalling or persisting.
func SaveTokenToUserCache(ctx context.Context, token *oauth2.Token) error {
	return cache.SaveToUserCache(ctx, token, tokenFile)
}

// LoadTokenFromUserCache gets the token instance from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadTokenFromUserCache(ctx context.Context) (*oauth2.Token, error) {
	var token oauth2.Token
	err := cache.LoadFromUserCache(ctx, &token, tokenFile)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// DeleteTokenFromUserCache removes token cache if existing or returns an error
func DeleteTokenFromUserCache(ctx context.Context) error {
	return cache.DeleteFromUserCache(ctx, tokenFile)
}
