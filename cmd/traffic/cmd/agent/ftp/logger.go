package ftp

import (
	"context"
	"fmt"

	ftplog "github.com/fclairamb/go-log"

	"github.com/datawire/dlib/dlog"
)

type logger struct {
	context.Context
}

// Logger creates a github.com/fclairamb/go-log Logger that sends its output
// to a dlog Logger
func Logger(ctx context.Context) ftplog.Logger {
	return &logger{ctx}
}

func (d *logger) Debug(event string, keyvals ...any) {
	d.log(dlog.LogLevelDebug, event, keyvals)
}

func (d *logger) Info(event string, keyvals ...any) {
	d.log(dlog.LogLevelInfo, event, keyvals)
}

func (d *logger) Warn(event string, keyvals ...any) {
	d.log(dlog.LogLevelWarn, event, keyvals)
}

func (d *logger) Error(event string, keyvals ...any) {
	d.log(dlog.LogLevelError, event, keyvals)
}

func (d *logger) Panic(event string, keyvals ...any) {
	d.log(dlog.LogLevelError, event, keyvals)
	panic(event)
}

func (d *logger) With(keyvals ...any) ftplog.Logger {
	return d.with(keyvals)
}

func (d *logger) log(level dlog.LogLevel, event string, keyvals []any) {
	if len(keyvals) > 0 {
		// Don't create a logger with fields on the fly if the max level
		// discards the output.
		if dlog.MaxLogLevel(d) >= level {
			dlog.Log(d.with(keyvals), level, event)
		}
	} else {
		dlog.Log(d, level, event)
	}
}

func (d *logger) with(keyvals []any) *logger {
	l := len(keyvals)
	if l == 0 {
		return d
	}
	if l%2 != 0 {
		keyvals = append(keyvals, "*** missing value ***")
		l++
	}
	c := d.Context
	for i := 0; i < l; i += 2 {
		c = dlog.WithField(c, fmt.Sprint(keyvals[i]), keyvals[i+1])
	}
	return &logger{c}
}
