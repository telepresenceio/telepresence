package client

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// Environment variable prefix for additional metadata to be reported
const environmentMetadataPrefix = "TELEPRESENCE_REPORT_"

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

// getInstallIDFromFilesystem returns the telepresence install ID, and also sets the reporter base
// metadata to include any conflicting install IDs written by old versions of the product.
func getInstallIDFromFilesystem(ctx context.Context, reporter *metriton.Reporter) (string, error) {
	type filecacheEntry struct {
		Body string
		Err  error
	}
	filecache := make(map[string]filecacheEntry)
	readFile := func(filename string) (string, error) {
		if _, isCached := filecache[filename]; !isCached {
			bs, err := ioutil.ReadFile(filename)
			filecache[filename] = filecacheEntry{
				Body: string(bs),
				Err:  err,
			}
		}
		return filecache[filename].Body, filecache[filename].Err
	}

	// Do these in order of precedence when there are multiple install IDs.
	var retID string
	allIDs := make(map[string]string)

	if runtime.GOOS != "windows" { // won't find any legacy on windows
		// We'll use this (and justify overriding GOOS=linux) below.
		xdgConfigHome, err := filelocation.UserConfigDir(filelocation.WithGOOS(ctx, "linux"))
		if err == nil {
			// Similarly to Telepresence-1 (below), edgectl always used the XDG filepath, but unlike
			// Telepresence-1 it did obey $XDG_CONFIG_HOME.
			if id, err := readFile(filepath.Join(xdgConfigHome, "edgectl", "id")); err == nil {
				allIDs["edgectl"] = id
				retID = id
			}
		}

		// Telepresence-1 used "$HOME/.config/telepresence/id" always, even on macOS (where ~/.config
		// isn't a thing) or when $XDG_CONFIG_HOME is something different than "$HOME/.config".
		if homeDir, err := filelocation.UserHomeDir(ctx); err == nil {
			if id, err := readFile(filepath.Join(homeDir, ".config", "telepresence", "id")); err == nil {
				allIDs["telepresence-1"] = id
				retID = id
			}
		}

		// Telepresence-2 prior to 2.1.0 did the exact same thing as edgectl, but with
		// "telepresence2" in the path instead of "edgectl".
		if id, err := readFile(filepath.Join(xdgConfigHome, "telepresence2", "id")); err == nil {
			allIDs["telepresence-2<2.1"] = id
			retID = id
		}
	}

	// Current.  Telepresence-2 now uses the most appropriate directory for the platform, and
	// uses "telepresence" instead of "telepresence2".  On GOOS=linux this is probably
	// (depending on how $XDG_CONFIG_HOME is set) the same as the Telepresence 1 location.
	telConfigDir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return "", err
	}
	idFilename := filepath.Join(telConfigDir, "id")
	if id, err := readFile(idFilename); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
	} else {
		allIDs["telepresence-2"] = id
		retID = id
	}

	// OK, now process all of that.

	if len(allIDs) == 0 {
		retID = uuid.New().String()
	}

	if allIDs["telepresence-2"] != retID {
		if err := os.MkdirAll(filepath.Dir(idFilename), 0755); err != nil {
			return "", err
		}
		if err := ioutil.WriteFile(idFilename, []byte(retID), 0644); err != nil {
			return "", err
		}
	}

	reporter.BaseMetadata["new_install"] = len(allIDs) == 0
	for product, id := range allIDs {
		if id != retID {
			reporter.BaseMetadata["install_id_"+product] = id
		}
	}
	return retID, nil
}

// NewScout creates a new initialized Scout instance that can be used to
// send telepresence reports to Metriton
func NewScout(ctx context.Context, mode string) (s *Scout) {
	return &Scout{
		Reporter: &metriton.Reporter{
			Application: "telepresence2",
			Version:     Version(),
			GetInstallID: func(r *metriton.Reporter) (string, error) {
				id, err := getInstallIDFromFilesystem(ctx, r)
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
				"goos":     runtime.GOOS,
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
func (s *Scout) Report(ctx context.Context, action string, meta ...ScoutMeta) {
	s.index++
	metadata := getDefaultEnvironmentMetadata()
	metadata["action"] = action
	metadata["index"] = s.index
	userInfo, err := authdata.LoadUserInfoFromUserCache(ctx)
	if err == nil && userInfo.Id != "" {
		metadata["user_id"] = userInfo.Id
		metadata["account_id"] = userInfo.AccountId
	}
	for _, metaItem := range meta {
		metadata[metaItem.Key] = metaItem.Value
	}

	_, err = s.Reporter.Report(ctx, metadata)
	if err != nil && ctx.Err() == nil {
		dlog.Infof(ctx, "scout report %q failed: %v", action, err)
	}
}

// Returns a metadata map containing all the additional environment variables to be reported
func getDefaultEnvironmentMetadata() map[string]interface{} {
	metadata := map[string]interface{}{}
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if strings.HasPrefix(pair[0], environmentMetadataPrefix) {
			key := strings.ToLower(strings.TrimPrefix(pair[0], environmentMetadataPrefix))
			metadata[key] = pair[1]
		}
	}
	return metadata
}
