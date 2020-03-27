package main

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/metriton"
)

type Scout struct {
	index    int
	reporter *metriton.Reporter
}

type ScoutMeta struct {
	Key   string
	Value interface{}
}

func NewScout(mode string) (s *Scout) {
	return &Scout{
		reporter: &metriton.Reporter{
			Application: "edgectl",
			Version:     Version,
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

func (s *Scout) SetMetadatum(key string, value interface{}) {
	oldValue, ok := s.reporter.BaseMetadata[key]
	if ok {
		panic(fmt.Sprintf("trying to replace metadata[%q] = %q with %q", key, oldValue, value))
	}
	s.reporter.BaseMetadata[key] = value
}

func (s *Scout) Report(action string, meta ...ScoutMeta) error {
	// Construct the report's metadata. Include the fixed (growing) set of
	// metadata in the Scout structure and the pairs passed as arguments to this
	// call. Also include and increment the index, which can be used to
	// determine the correct order of reported events for this installation
	// attempt (correlated by the trace_id set at the start).
	s.index++
	metadata := map[string]interface{}{
		"action": action,
		"index":  s.index,
	}
	for _, metaItem := range meta {
		metadata[metaItem.Key] = metaItem.Value
	}

	_, err := s.reporter.Report(metadata)
	if err != nil {
		return errors.Wrap(err, "scout report")
	}
	// TODO: Do something useful (alert the user if there's an available
	// upgrade?) with the response (discarded as "_" above)?

	return nil
}
