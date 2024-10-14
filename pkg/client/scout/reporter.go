package scout

import (
	"context"

	"github.com/blang/semver/v4"
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

// Entry is a key/value association used when reporting.
type Entry struct {
	Key   string
	Value any
}

type reporterKey struct{}

func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, reporter)
}

type sessionKey struct{}

type session interface {
	ManagerVersion() semver.Version
	Done() <-chan struct{}
}

func WithSession(ctx context.Context, s session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func getReporter(ctx context.Context) Reporter {
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok {
		return r
	}
	return nil
}

// NewReporter creates a new initialized Reporter instance that can be used to
// send telepresence reports to Metriton and assigns it to the current context.
//
//nolint:gochecknoglobals // extension point
var NewReporter = func(ctx context.Context, mode string) context.Context {
	return ctx
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
	<-ctx.Done()
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
