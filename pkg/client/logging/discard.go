package logging

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

func (d discard) Log(_ dlog.LogLevel, _ string) {
}

// WithDiscardingLogger returns a context that discards all log output
func WithDiscardingLogger(ctx context.Context) context.Context {
	return dlog.WithLogger(ctx, discard(0))
}
