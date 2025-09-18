package trafficmgr

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func sessionInfoFile(daemonID *daemon.Identifier) string {
	return filepath.Join("sessions", daemonID.InfoFileName())
}

type savedSession struct {
	KubeContext string               `json:"kubeContext"`
	Namespace   string               `json:"namespace"`
	Session     *manager.SessionInfo `json:"session"`
}

// LoadSessionInfoFromUserCache gets the SessionInfo from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadSessionInfoFromUserCache(ctx context.Context, daemonID *daemon.Identifier) (*manager.SessionInfo, error) {
	var ss *savedSession
	err := cache.LoadFromUserCache(ctx, &ss, sessionInfoFile(daemonID))
	if err == nil && ss.KubeContext == daemonID.KubeContext && ss.Namespace == daemonID.Namespace {
		return ss.Session, nil
	}
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return nil, err
}

// saveSessionInfoToUserCache saves the provided SessionInfo to user cache and returns an error if
// something goes wrong while marshalling or persisting.
func saveSessionInfoToUserCache(ctx context.Context, daemonID *daemon.Identifier, session *manager.SessionInfo) error {
	return cache.SaveToUserCache(ctx, &savedSession{
		KubeContext: daemonID.KubeContext,
		Namespace:   daemonID.Namespace,
		Session:     session,
	}, sessionInfoFile(daemonID), cache.Public)
}

// deleteSessionInfoFromUserCache removes SessionInfo cache if existing or returns an error. An attempt
// to remove a non-existing cache is a no-op and the function returns nil.
func deleteSessionInfoFromUserCache(ctx context.Context, daemonID *daemon.Identifier) error {
	return cache.DeleteFromUserCache(ctx, sessionInfoFile(daemonID))
}
