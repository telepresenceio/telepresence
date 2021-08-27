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

func (d discard) WithField(_ string, _ interface{}) dlog.Logger {
	return d
}

func (d discard) StdLogger(_ dlog.LogLevel) *log.Logger {
	return log.New(io.Discard, "", 0)
}

func (d discard) Log(_ dlog.LogLevel, _ ...interface{}) {
}

func (d discard) Logf(_ dlog.LogLevel, _ string, _ ...interface{}) {
}

func (d discard) Logln(_ dlog.LogLevel, _ ...interface{}) {
}

// WithDiscardingLogger returns a context that discards all log output
func WithDiscardingLogger(ctx context.Context) context.Context {
	return dlog.WithLogger(ctx, discard(0))
}
