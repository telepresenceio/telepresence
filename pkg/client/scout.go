package client

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

// Scout is a Metriton reported
type Scout struct {
	index    int
	Reporter *metriton.Reporter
}

// ScoutMeta is a key/value association used when reporting
type ScoutMeta struct {
	Key   string
	Value interface{}
}

// NewScout creates a new initialized Scout instance that can be used to
// send telepresence reports to Metriton
func NewScout(_ context.Context, mode string) (s *Scout) {
	return &Scout{
		Reporter: &metriton.Reporter{
			Application: "telepresence2",
			Version:     Version(),
			GetInstallID: func(r *metriton.Reporter) (string, error) {
				id, err := metriton.InstallIDFromFilesystem(r)
				if err != nil {
					id = "00000000-0000-0000-0000-000000000000"
					r.BaseMetadata["new_install"] = true
					r.BaseMetadata["install_id_error"] = err.Error()
				}
				return id, nil
			},
			// Fixed (growing) metadata passed with every report
			BaseMetadata: map[string]interface{}{
				"mode":     mode,
				"trace_id": uuid.New().String(),
			},
		},
	}
}

// SetMetadatum associates the given key with the given value in the metadata
// of this instance. It's an error if the key already exists.
func (s *Scout) SetMetadatum(key string, value interface{}) {
	oldValue, ok := s.Reporter.BaseMetadata[key]
	if ok {
		panic(fmt.Sprintf("trying to replace metadata[%q] = %q with %q", key, oldValue, value))
	}
	s.Reporter.BaseMetadata[key] = value
}

// Report constructs and sends a report. It includes the fixed (growing) set of
// metadata in the Scout structure and the pairs passed as arguments to this
// call. It also includes and increments the index, which can be used to
// determine the correct order of reported events for this installation
// attempt (correlated by the trace_id set at the start).
func (s *Scout) Report(ctx context.Context, action string, meta ...ScoutMeta) error {
	s.index++
	metadata := map[string]interface{}{
		"action": action,
		"index":  s.index,
	}
	userInfo, err := cache.LoadUserInfoFromUserCache(ctx)
	if err == nil && userInfo.Id != "" {
		metadata["user_id"] = userInfo.Id
		metadata["account_id"] = userInfo.AccountId
	}
	for _, metaItem := range meta {
		metadata[metaItem.Key] = metaItem.Value
	}

	_, err = s.Reporter.Report(ctx, metadata)
	if err != nil {
		return errors.Wrap(err, "scout report")
	}
	// TODO: Do something useful (alert the user if there's an available
	// upgrade?) with the response (discarded as "_" above)?

	return nil
}
