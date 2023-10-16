package trafficmgr

import (
	"context"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func sessionInfoFile(daemonID *daemon.Identifier) string {
	return filepath.Join("sessions", daemonID.InfoFileName())
}

type SavedSession struct {
	KubeContext string               `json:"kubeContext"`
	Namespace   string               `json:"namespace"`
	Session     *manager.SessionInfo `json:"session"`
}

// SaveSessionInfoToUserCache saves the provided SessionInfo to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func SaveSessionInfoToUserCache(ctx context.Context, daemonID *daemon.Identifier, session *manager.SessionInfo) error {
	return cache.SaveToUserCache(ctx, &SavedSession{
		KubeContext: daemonID.KubeContext,
		Namespace:   daemonID.Namespace,
		Session:     session,
	}, sessionInfoFile(daemonID), cache.Public)
}

// LoadSessionInfoFromUserCache gets the SessionInfo from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadSessionInfoFromUserCache(ctx context.Context, daemonID *daemon.Identifier) (*manager.SessionInfo, error) {
	var ss *SavedSession
	err := cache.LoadFromUserCache(ctx, &ss, sessionInfoFile(daemonID))
	if err == nil && ss.KubeContext == daemonID.KubeContext && ss.Namespace == daemonID.Namespace {
		return ss.Session, nil
	}
	if err != nil && os.IsNotExist(err) {
		err = nil
	}
	return nil, err
}

// DeleteSessionInfoFromUserCache removes SessionInfo cache if existing or returns an error. An attempt
// to remove a non-existing cache is a no-op and the function returns nil.
func DeleteSessionInfoFromUserCache(ctx context.Context, daemonID *daemon.Identifier) error {
	return cache.DeleteFromUserCache(ctx, sessionInfoFile(daemonID))
}
