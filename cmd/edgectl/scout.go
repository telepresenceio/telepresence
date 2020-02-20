package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

type Scout struct {
	installID string
	index     int
	metadata  map[string]interface{}
}

type ScoutMeta struct {
	Key   string
	Value interface{}
}

func NewScout(mode string) (s *Scout) {
	// Fixed (growing) metadata passed with every report
	metadata := make(map[string]interface{})
	metadata["mode"] = mode
	metadata["trace_id"] = uuid.New().String()

	// The result
	s = &Scout{metadata: metadata}

	// Get or create the persistent install ID for Edge Control
	err := func() error {
		// We store the persistent ID in ~/.config/edgectl/id to be consistent
		// with Telepresence. We could instead use os.UserConfigDir() as the
		// root of the destination, which may be the same on Linux, but is
		// definitely different on MacOS.
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		configDir := filepath.Join(home, ".config", "edgectl")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return err
		}
		idFilename := filepath.Join(configDir, "id")

		// Try to read an existing install ID
		if installIDBytes, err := ioutil.ReadFile(idFilename); err == nil {
			s.installID = string(installIDBytes)
			// Validate UUID format
			if _, err := uuid.Parse(s.installID); err == nil {
				metadata["new_install"] = false
				return nil
			} // else the value is not a UUID
		} // else file doesn't exist, etc.

		// Try to create a new install ID
		s.installID = uuid.New().String()
		metadata["new_install"] = true
		if err := ioutil.WriteFile(idFilename, []byte(s.installID), 0755); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		s.installID = "00000000-0000-0000-0000-000000000000"
		metadata["new_install"] = true
		metadata["install_id_error"] = err.Error()
	}

	return
}

func (s *Scout) SetMetadatum(key string, value interface{}) {
	oldValue, ok := s.metadata[key]
	if ok {
		panic(fmt.Sprintf("trying to replace metadata[%q] = %q with %q", key, oldValue, value))
	}
	s.metadata[key] = value
}

func (s *Scout) Report(action string, meta ...ScoutMeta) error {
	if os.Getenv("SCOUT_DISABLE") != "" {
		return nil
	}

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
	for metaKey, metaValue := range s.metadata {
		metadata[metaKey] = metaValue
	}
	for _, metaItem := range meta {
		metadata[metaItem.Key] = metaItem.Value
	}

	data := map[string]interface{}{
		"application": "edgectl",
		"install_id":  s.installID,
		"version":     Version,
		"metadata":    metadata,
	}

	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		panic(err)
	}
	metritonEndpoint := "https://metriton.datawire.io/scout"
	resp, err := http.Post(metritonEndpoint, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return errors.Wrap(err, "scout report")
	}

	// TODO: Do something useful (?) with the response?
	_, _ = ioutil.ReadAll(resp.Body)
	_ = resp.Body.Close()

	return nil
}
