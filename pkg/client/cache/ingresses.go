package cache

import (
	"context"
	"os"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

const ingressesFile = "ingresses.json"

// SaveIngressesToUserCache saves the provided ingresses to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveIngressesToUserCache(ctx context.Context, ingresses map[string]*manager.IngressInfo) error {
	if len(ingresses) == 0 {
		return DeleteIngressesFromUserCache(ctx)
	}
	return SaveToUserCache(ctx, ingresses, ingressesFile)
}

// LoadIngressesFromUserCache gets the ingresses from cache. An empty map is returned if the
// file does not exist. An error is returned if something goes wrong while loading or unmarshalling.
func LoadIngressesFromUserCache(ctx context.Context) (map[string]*manager.IngressInfo, error) {
	var ingresses map[string]*manager.IngressInfo
	err := LoadFromUserCache(ctx, &ingresses, ingressesFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return make(map[string]*manager.IngressInfo), nil
	}
	return ingresses, nil
}

// DeleteIngressesFromUserCache removes the ingresses cache if exists or returns an error. An attempt
// to remove a non existing cache is a no-op and the function returns nil.
func DeleteIngressesFromUserCache(ctx context.Context) error {
	return DeleteFromUserCache(ctx, ingressesFile)
}
