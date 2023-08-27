package trafficmgr

import (
	"context"
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

const sessionInfoFile = "session-%s.json"

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
	}, fmt.Sprintf(sessionInfoFile, daemonID.String()))
}

// LoadSessionInfoFromUserCache gets the SessionInfo from cache or returns an error if something goes
// wrong while loading or unmarshalling.
func LoadSessionInfoFromUserCache(ctx context.Context, daemonID *daemon.Identifier) (*manager.SessionInfo, error) {
	var ss *SavedSession
	err := cache.LoadFromUserCache(ctx, &ss, fmt.Sprintf(sessionInfoFile, daemonID.String()))
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
	return cache.DeleteFromUserCache(ctx, fmt.Sprintf(sessionInfoFile, daemonID.String()))
}
