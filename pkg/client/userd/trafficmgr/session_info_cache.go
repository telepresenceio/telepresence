package trafficmgr

import (
	"context"
	"os"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const sessionInfoFile = "sessions.json"

type SavedSession struct {
	Host    string
	Session *manager.SessionInfo `json:"session"`
}

type userSessionCacheImpl struct{}

// SaveSessionInfoToUserCache saves the provided SessionInfo to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func (s *userSessionCacheImpl) SaveSessionInfoToUserCache(ctx context.Context, host string, session *manager.SessionInfo) error {
	return cache.SaveToUserCache(ctx, &SavedSession{
		Host:    host,
		Session: session,
	}, sessionInfoFile)
}

// LoadSessionInfoFromUserCache gets the SessionInfo from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func (s *userSessionCacheImpl) LoadSessionInfoFromUserCache(ctx context.Context, host string) (*manager.SessionInfo, error) {
	var ss *SavedSession
	err := cache.LoadFromUserCache(ctx, &ss, sessionInfoFile)
	if err == nil && ss.Host == host {
		return ss.Session, nil
	}
	if err != nil && os.IsNotExist(err) {
		err = nil
	}
	return nil, err
}

// DeleteSessionInfoFromUserCache removes SessionInfo cache if existing or returns an error. An attempt
// to remove a non-existing cache is a no-op and the function returns nil.
func (s *userSessionCacheImpl) DeleteSessionInfoFromUserCache(ctx context.Context) error {
	return cache.DeleteFromUserCache(ctx, sessionInfoFile)
}
