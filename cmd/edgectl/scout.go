package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

type Scout struct {
	mode       string
	installID  string
	newInstall bool
}

type ScoutMeta struct {
	Key   string
	Value interface{}
}

func NewScout(mode string) (*Scout, error) {
	var installID string
	var newInstall bool

	// Get or create the persistent install ID for Edge Control
	err := func() error {
		// We store the persistent ID in ~/.config/edgectl/id to be consistent
		// with Telepresence. We could instead use os.UserConfigDir() as the
		// root of the destination, which may be the same on Linux, but is
		// definitely different on MacOS.
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.Wrap(err, "install ID")
		}
		configDir := filepath.Join(home, ".config", "edgectl")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return errors.Wrap(err, "install ID")
		}
		idFilename := filepath.Join(configDir, "id")

		// Try to read an existing install ID
		if installIDBytes, err := ioutil.ReadFile(idFilename); err == nil {
			installID = string(installIDBytes)
			// Validate UUID format
			if _, err := uuid.Parse(installID); err == nil {
				newInstall = false
				return nil
			} // else the value is not a UUID
		} // else file doesn't exist, etc.

		// Try to create a new install ID
		installID = uuid.New().String()
		if err := ioutil.WriteFile(idFilename, []byte(installID), 0755); err != nil {
			return errors.Wrap(err, "install ID")
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}

	return &Scout{mode, installID, newInstall}, nil
}

func (s *Scout) Report(action string, meta ...ScoutMeta) error {
	if os.Getenv("SCOUT_DISABLE") != "" {
		return nil
	}

	metadata := map[string]interface{}{
		"mode":        s.mode,
		"new_install": s.newInstall,
		"action":      action,
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
	// metritonEndpoint := "https://metriton.datawire.io/scout"   // Prod URL
	metritonEndpoint := "https://metriton.datawire.io/beta/scout" // Beta URL
	resp, err := http.Post(metritonEndpoint, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return errors.Wrap(err, "scout report")
	}

	// TODO: Do something useful (?) with the response?
	_, _ = ioutil.ReadAll(resp.Body)
	_ = resp.Body.Close()

	return nil
}
