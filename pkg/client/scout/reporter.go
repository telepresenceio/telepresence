package scout

import (
	"context"
	"net"
	"net/url"
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
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// EnvironmentMetadataPrefix is the Environment variable prefix for additional metadata to be reported.
const EnvironmentMetadataPrefix = "TELEPRESENCE_REPORT_"

type bufEntry struct {
	action  string
	entries []Entry
}

type (
	ReportAnnotator func(map[string]any)
	ReportMutator   func(context.Context, []Entry) []Entry
)

// Reporter is a Metriton reporter.
type Reporter interface {
	Close()
	InstallID() string
	Report(ctx context.Context, action string, entries ...Entry)
	Run(ctx context.Context) error
	SetMetadatum(ctx context.Context, key string, value any)
	Start(ctx context.Context)
}

type reporter struct {
	index            int
	buffer           chan bufEntry
	done             chan struct{}
	reportAnnotators []ReportAnnotator
	reportMutators   []ReportMutator
	reporter         *metriton.Reporter
}

// Entry is a key/value association used when reporting.
type Entry struct {
	Key   string
	Value any
}

type InstallType string

const (
	CLI    InstallType = "cli"
	Docker InstallType = "docker"
)

var idFiles = map[InstallType]string{ //nolint:gochecknoglobals // constant
	CLI:    "id",
	Docker: "docker_id",
}

// setInstallIDFromFilesystem sets the telepresence install ID in the given map, including any conflicting
// install IDs written by old versions of the product.
//
//nolint:gochecknoglobals // can be overridden for test purposes
var setInstallIDFromFilesystem = func(ctx context.Context, installType InstallType, md map[string]any) (string, error) {
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
		xdgConfigHome := filelocation.UserConfigDir(filelocation.WithGOOS(ctx, "linux"))
		// Similarly to Telepresence-1 (below), edgectl always used the XDG filepath, but unlike
		// Telepresence-1 it did obey $XDG_CONFIG_HOME.
		if id, err := readFile(filepath.Join(xdgConfigHome, "edgectl", "id")); err == nil {
			allIDs["edgectl"] = id
			retID = id
		}

		// Telepresence-1 used "$HOME/.config/telepresence/id" always, even on macOS (where ~/.config
		// isn't a thing) or when $XDG_CONFIG_HOME is something different than "$HOME/.config".
		if id, err := readFile(filepath.Join(filelocation.UserHomeDir(ctx), ".config", "telepresence", "id")); err == nil {
			allIDs["telepresence-1"] = id
			retID = id
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
	telConfigDir := filelocation.AppUserConfigDir(ctx)
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
		if err := os.MkdirAll(filepath.Dir(idFilename), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(idFilename, []byte(retID), 0o644); err != nil {
			return "", err
		}
	}

	md["new_install"] = len(allIDs) == 0

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
			md["install_id_"+product] = id
		}
	}
	return retID, nil
}

// bufferSize is the max number of entries that can be stored in the buffer
// before entries are discarded.
const bufferSize = 40

func NewReporterForInstallType(ctx context.Context, mode string, installType InstallType, reportAnnotators []ReportAnnotator, reportMutators []ReportMutator) Reporter {
	md := make(map[string]any, 12)
	setOsMetadata(ctx, md)
	installID, err := setInstallIDFromFilesystem(ctx, installType, md)
	if err != nil {
		installID = "00000000-0000-0000-0000-000000000000"
		md["new_install"] = true
		md["install_id_error"] = err.Error()
	}
	// Fixed (growing) metadata passed with every report
	md["mode"] = mode
	md["trace_id"] = uuid.NewString() //  It's sent as JSON so might as well convert it to a string once here.
	md["goos"] = runtime.GOOS
	md["goarch"] = runtime.GOARCH

	// Discover how Telepresence was installed based on the binary's location
	installMethod, err := client.GetInstallMechanism()
	if err != nil {
		dlog.Errorf(ctx, "scout error getting executable: %s", err)
	}
	md["install_method"] = installMethod
	setDefaultEnvironmentMetadata(md)

	if env := client.GetEnv(ctx); env != nil && !env.ScoutDisable {
		// Some tests disable scout reporting by setting the host IP to 127.0.0.1. This spams
		// the logs with lots of "connection refused" messages and makes them hard to read.
		mh, _ := url.Parse(metriton.DefaultEndpoint)
		luc, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if ips, err := net.DefaultResolver.LookupIP(luc, "tcp", mh.Host); err == nil {
			if ips[0].Equal(net.IP{127, 0, 0, 1}) {
				env.ScoutDisable = true
				_ = os.Setenv("SCOUT_DISABLE", "1")
			}
		}
	}

	return &reporter{
		reporter: &metriton.Reporter{
			Application: "telepresence2",
			Version:     client.Version(),
			GetInstallID: func(r *metriton.Reporter) (string, error) {
				return installID, nil
			},
			BaseMetadata: md,
		},
		reportAnnotators: reportAnnotators,
		reportMutators:   reportMutators,
		buffer:           make(chan bufEntry, bufferSize),
		done:             make(chan struct{}),
	}
}

// DefaultReportAnnotators are the default annotator functions that the NewReporter function will pass to NewReporterForInstallType.
var DefaultReportAnnotators []ReportAnnotator //nolint:gochecknoglobals // extension point

// DefaultReportMutators are the default mutator functions that the NewReporter function will pass to NewReporterForInstallType.
var DefaultReportMutators []ReportMutator = []ReportMutator{sessionReportMutator} //nolint:gochecknoglobals // extension point

// NewReporter creates a new initialized Reporter instance that can be used to
// send telepresence reports to Metriton and assigns it to the current context.
func NewReporter(ctx context.Context, mode string) context.Context {
	return WithReporter(ctx, NewReporterForInstallType(ctx, mode, CLI, DefaultReportAnnotators, DefaultReportMutators))
}

func InstallID(ctx context.Context) string {
	if r := getReporter(ctx); r != nil {
		return r.InstallID()
	}
	return ""
}

func Close(ctx context.Context) {
	if r := getReporter(ctx); r != nil {
		r.Close()
	}
}

// Run ensures that all reports on the send queue are sent to the endpoint.
func Run(ctx context.Context) error {
	if r := getReporter(ctx); r != nil {
		return r.Run(ctx)
	}
	return nil
}

// Start runs the Reporter found in the current context in a goroutine.
func Start(ctx context.Context) {
	if r := getReporter(ctx); r != nil {
		r.Start(ctx)
	}
}

// Report sends a report using the Reporter found in the current context.
func Report(ctx context.Context, action string, entries ...Entry) {
	if r := getReporter(ctx); r != nil {
		r.Report(ctx, action, entries...)
	}
}

// SetMetadatum associates the given key with the given value in the metadata
// of the Reporter found in the current context.
func SetMetadatum(ctx context.Context, key string, value any) {
	if r := getReporter(ctx); r != nil {
		r.SetMetadatum(ctx, key, value)
	}
}

func (r *reporter) InstallID() string {
	return r.reporter.InstallID()
}

const setMetadatumAction = "__set_metadatum__"

// SetMetadatum associates the given key with the given value in the metadata
// of this instance.
func (r *reporter) SetMetadatum(ctx context.Context, key string, value any) {
	r.Report(ctx, setMetadatumAction, Entry{Key: key, Value: value})
}

// Start runs the instance in a goroutine.
func (r *reporter) Start(ctx context.Context) {
	go func() {
		if err := r.Run(ctx); err != nil {
			dlog.Error(ctx, err)
		}
	}()
}

func (r *reporter) Close() {
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

// Run ensures that all reports on the send queue are sent to the endpoint.
func (r *reporter) Run(ctx context.Context) error {
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
func (r *reporter) Report(ctx context.Context, action string, entries ...Entry) {
	for _, m := range r.reportMutators {
		// IDEA allow mutator to cancel report
		entries = m(ctx, entries)
	}
	select {
	case r.buffer <- bufEntry{action, entries}:
	default:
		dlog.Infof(ctx, "scout report %q discarded. Output buffer is full (or closed)", action)
	}
}

func (r *reporter) doReport(ctx context.Context, be *bufEntry) {
	r.index++
	metadata := make(map[string]any, 4+len(be.entries))
	metadata["action"] = be.action
	metadata["index"] = r.index
	for _, ra := range r.reportAnnotators {
		ra(metadata)
	}
	for _, metaItem := range be.entries {
		metadata[metaItem.Key] = metaItem.Value
	}

	_, err := r.reporter.Report(ctx, metadata)
	if err != nil && ctx.Err() == nil {
		dlog.Infof(ctx, "scout report %q failed: %v", be.action, err)
	}
}

// setDefaultEnvironmentMetadata sets all the additional environment variables to be reported.
func setDefaultEnvironmentMetadata(metadata map[string]any) {
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if strings.HasPrefix(pair[0], EnvironmentMetadataPrefix) {
			key := strings.ToLower(strings.TrimPrefix(pair[0], EnvironmentMetadataPrefix))
			metadata[key] = pair[1]
		}
	}
}
