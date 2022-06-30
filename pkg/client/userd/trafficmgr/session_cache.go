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

// SaveSessionToUserCache saves the provided session to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveSessionToUserCache(ctx context.Context, host string, session *manager.SessionInfo) error {
	return cache.SaveToUserCache(ctx, &SavedSession{
		Host:    host,
		Session: session,
	}, sessionInfoFile)
}

// LoadSessionFromUserCache gets the session from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadSessionFromUserCache(ctx context.Context, host string) (*manager.SessionInfo, error) {
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

// DeleteSessionFromUserCache removes user info cache if existing or returns an error. An attempt
// to remove a non-existing cache is a no-op and the function returns nil.
func DeleteSessionFromUserCache(ctx context.Context) error {
	return cache.DeleteFromUserCache(ctx, sessionInfoFile)
}
