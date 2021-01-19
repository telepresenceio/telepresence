package cache

import (
	"os"

	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

const ingressesFile = "ingresses.json"

// SaveIngressesToUserCache saves the provided ingresses to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveIngressesToUserCache(ingresses map[string]*manager.IngressInfo) error {
	if len(ingresses) == 0 {
		return DeleteIngressesFromUserCache()
	}
	return saveToUserCache(ingresses, ingressesFile)
}

// LoadIngressesFromUserCache gets the ingresses from cache. An empty map is returned if the
// file does not exist. An error is returned if something goes wrong while loading or unmarshalling.
func LoadIngressesFromUserCache() (map[string]*manager.IngressInfo, error) {
	var ingresses map[string]*manager.IngressInfo
	err := loadFromUserCache(&ingresses, ingressesFile)
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
func DeleteIngressesFromUserCache() error {
	return deleteFromUserCache(ingressesFile)
}
