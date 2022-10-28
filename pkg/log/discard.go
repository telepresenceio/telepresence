package log

import (
	"context"
	"io"
	"log"

	"github.com/datawire/dlib/dlog"
)

type discard int

func (d discard) Helper() {
}

func (d discard) Log(_ dlog.LogLevel, _ string) {
}

// We need to implement the UnformattedXXX functions to prevent that
// dlog actually formats the messages prior to discarding them

func (d discard) UnformattedLog(_ dlog.LogLevel, _ ...any) {
}

func (d discard) UnformattedLogf(_ dlog.LogLevel, _ string, _ ...any) {
}

func (d discard) UnformattedLogln(_ dlog.LogLevel, _ ...any) {
}

func (d discard) StdLogger(_ dlog.LogLevel) *log.Logger {
	return log.New(io.Discard, "", 0)
}

func (d discard) WithField(_ string, _ any) dlog.Logger {
	return d
}

// WithDiscardingLogger returns a context that discards all log output.
func WithDiscardingLogger(ctx context.Context) context.Context {
	return dlog.WithLogger(ctx, discard(0))
}
