package scout

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/metriton-go-client/metriton"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// Environment variable prefix for additional metadata to be reported
const environmentMetadataPrefix = "TELEPRESENCE_REPORT_"

type bufEntry struct {
	action  string
	entries []Entry
}

// Reporter is a Metriton reported
type Reporter struct {
	index    int
	buffer   chan bufEntry
	done     chan struct{}
	reporter *metriton.Reporter
}

// Entry is a key/value association used when reporting
type Entry struct {
	Key   string
	Value interface{}
}

type InstallType string

const (
	CLI    InstallType = "cli"
	Docker InstallType = "docker"
)

var idFiles = map[InstallType]string{
	CLI:    "id",
	Docker: "docker_id",
}

// getInstallIDFromFilesystem returns the telepresence install ID, and also sets the reporter base
// metadata to include any conflicting install IDs written by old versions of the product.
func getInstallIDFromFilesystem(ctx context.Context, reporter *metriton.Reporter, installType InstallType) (string, error) {
	type filecacheEntry struct {
		Body string
		Err  error
	}
	filecache := make(map[string]filecacheEntry)
	readFile := func(filename string) (string, error) {
		if _, isCached := filecache[filename]; !isCached {
			bs, err := os.ReadFile(filename)
			filecache[filename] = filecacheEntry{
				Body: strings.TrimSpace(string(bs)),
				Err:  err,
			}
		}
		return filecache[filename].Body, filecache[filename].Err
	}

	// Do these in order of precedence when there are multiple install IDs.
	var retID string
	allIDs := make(map[string]string)

	if runtime.GOOS != "windows" { // won't find any legacy on Windows
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
	idFilename := filepath.Join(telConfigDir, idFiles[installType])
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
		if err := os.WriteFile(idFilename, []byte(retID), 0644); err != nil {
			return "", err
		}
	}

	reporter.BaseMetadata["new_install"] = len(allIDs) == 0

	// We don't want to add the extra ids until we've decided if it's a new install or not
	// this is because we'd like a new install of type A to be reported even if there's already
	// an existing install of type B
	for otherType, fileName := range idFiles {
		if otherType == installType {
			continue
		}
		idFilename := filepath.Join(telConfigDir, fileName)
		if id, err := readFile(idFilename); err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
		} else {
			allIDs["telepresence-2-"+string(otherType)] = id
		}
	}

	for product, id := range allIDs {
		if id != retID {
			reporter.BaseMetadata["install_id_"+product] = id
		}
	}
	return retID, nil
}

// bufferSize is the max number of entries that can be stored in the buffer
// before entries are discarded.
const bufferSize = 40

func NewReporterForInstallType(ctx context.Context, mode string, installType InstallType) *Reporter {
	r := &Reporter{
		reporter: &metriton.Reporter{
			Application: "telepresence2",
			Version:     client.Version(),
			GetInstallID: func(r *metriton.Reporter) (string, error) {
				id, err := getInstallIDFromFilesystem(ctx, r, installType)
				if err != nil {
					id = "00000000-0000-0000-0000-000000000000"
					r.BaseMetadata["new_install"] = true
					r.BaseMetadata["install_id_error"] = err.Error()
				}
				return id, nil
			},
		},
	}
	r.initialize(ctx, mode, runtime.GOOS, runtime.GOARCH)
	return r
}

// NewReporter creates a new initialized Reporter instance that can be used to
// send telepresence reports to Metriton
func NewReporter(ctx context.Context, mode string) *Reporter {
	return NewReporterForInstallType(ctx, mode, CLI)
}

// initialization broken out or constructor for the benefit of testing
func (r *Reporter) initialize(ctx context.Context, mode, goos, goarch string) {
	r.buffer = make(chan bufEntry, bufferSize)
	r.done = make(chan struct{})

	// Fixed (growing) metadata passed with every report
	baseMeta := getOsMetadata(ctx)
	baseMeta["mode"] = mode
	baseMeta["trace_id"] = uuid.New()
	baseMeta["goos"] = goos
	baseMeta["goarch"] = goarch

	// Discover how Telepresence was installed based on the binary's location
	installMethod, err := client.GetInstallMechanism()
	if err != nil {
		dlog.Errorf(ctx, "scout error getting executable: %s", err)
	}
	baseMeta["install_method"] = installMethod
	for k, v := range getDefaultEnvironmentMetadata() {
		baseMeta[k] = v
	}
	r.reporter.BaseMetadata = baseMeta
}

func (r *Reporter) InstallID() string {
	return r.reporter.InstallID()
}

const setMetadatumAction = "__set_metadatum__"

// SetMetadatum associates the given key with the given value in the metadata
// of this instance.
func (r *Reporter) SetMetadatum(ctx context.Context, key string, value interface{}) {
	r.Report(ctx, setMetadatumAction, Entry{Key: key, Value: value})
}

// Start starts the instance in a goroutine
func (r *Reporter) Start(ctx context.Context) {
	go func() {
		if err := r.Run(ctx); err != nil {
			dlog.Error(ctx, err)
		}
	}()
}

func (r *Reporter) Close() {
	// Send a zeroed bufEntry here instead of closing the buffer so that
	// any stray Reports that arrive after the context is cancelled aren't
	// sent on a closed channel
	select {
	case r.buffer <- bufEntry{}:
	default:
	}
	// Wait for the done channel to close. Give up after 3 seconds (that
	// should be plenty)
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
	}
}

// Run ensures that all reports on the send queue are sent to the endpoint
func (r *Reporter) Run(ctx context.Context) error {
	go func() {
		// Close buffer and let it drain when ctx is done.
		<-ctx.Done()

		// Send a zeroed bufEntry here instead of closing the buffer so that
		// any stray Reports that arrive after the context is cancelled aren't
		// sent on a closed channel
		select {
		case r.buffer <- bufEntry{}:
		default:
		}
	}()

	defer close(r.done)

	hc := dcontext.HardContext(ctx)
	for be := range r.buffer {
		switch be.action {
		case "":
			return nil
		case setMetadatumAction:
			entry := be.entries[0]
			if entry.Value == "" {
				delete(r.reporter.BaseMetadata, entry.Key)
			} else {
				r.reporter.BaseMetadata[entry.Key] = entry.Value
			}
		default:
			r.doReport(hc, &be)
		}
	}
	return nil
}

// Report constructs and buffers a report on the send queue. It includes the fixed (growing)
// set of metadata in the Reporter structure and the entries passed as arguments to this
// call. It also includes and increments the index, which can be used to
// determine the correct order of reported events for this installation
// attempt (correlated by the trace_id set at the start).
func (r *Reporter) Report(ctx context.Context, action string, entries ...Entry) {
	select {
	case r.buffer <- bufEntry{action, entries}:
	default:
		dlog.Infof(ctx, "scout report %q discarded. Output buffer is full (or closed)", action)
	}
}

func (r *Reporter) doReport(ctx context.Context, be *bufEntry) {
	r.index++
	metadata := make(map[string]interface{}, 4+len(be.entries))
	metadata["action"] = be.action
	metadata["index"] = r.index
	userInfo, err := authdata.LoadUserInfoFromUserCache(ctx)
	if err == nil && userInfo.Id != "" {
		metadata["user_id"] = userInfo.Id
		metadata["account_id"] = userInfo.AccountId
	}
	for _, metaItem := range be.entries {
		metadata[metaItem.Key] = metaItem.Value
	}

	_, err = r.reporter.Report(ctx, metadata)
	if err != nil && ctx.Err() == nil {
		dlog.Infof(ctx, "scout report %q failed: %v", be.action, err)
	}
}

// Returns a metadata map containing all the additional environment variables to be reported
func getDefaultEnvironmentMetadata() map[string]string {
	metadata := map[string]string{}
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if strings.HasPrefix(pair[0], environmentMetadataPrefix) {
			key := strings.ToLower(strings.TrimPrefix(pair[0], environmentMetadataPrefix))
			metadata[key] = pair[1]
		}
	}
	return metadata
}
