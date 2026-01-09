package dslog

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"path"
	"runtime"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/dlib/v2/dlog"
)

//nolint:gochecknoglobals // constant
var dlogLevel2slogLevel = [5]slog.Level{
	slog.LevelError,
	slog.LevelWarn,
	slog.LevelInfo,
	slog.LevelDebug,
	slog.LevelDebug - 4,
}

func slogLevel(level dlog.LogLevel) slog.Level {
	if level > dlog.LogLevelTrace {
		panic(fmt.Errorf("invalid LogLevel: %d", level))
	}
	return dlogLevel2slogLevel[level]
}

type slogWrapper struct {
	context.Context
}

func (w slogWrapper) Helper() {
}

func (w slogWrapper) LogMessage(level dlog.LogLevel, message string) {
	clog.Logger(w).Log(context.Background(), slogLevel(level), message)
}

func (w slogWrapper) StdLogger(level dlog.LogLevel) *log.Logger {
	return clog.StdLogger(w, slogLevel(level))
}

func (w slogWrapper) Log(level dlog.LogLevel, args ...any) {
	lv := slogLevel(level)
	h := clog.Logger(w).Handler()
	if h.Enabled(w, lv) {
		var pcs [1]uintptr
		runtime.Callers(4, pcs[:]) // skip [Callers, Log, convenience]
		r := slog.NewRecord(time.Now(), slogLevel(level), fmt.Sprint(args...), pcs[0])
		_ = h.Handle(w, r)
	}
}

func (w slogWrapper) Logf(level dlog.LogLevel, format string, args ...any) {
	lv := slogLevel(level)
	lg := clog.Logger(w)
	if lg.Enabled(w, lv) {
		lg.Log(w, lv, fmt.Sprintf(format, args...))
	}
}

func (w slogWrapper) Logln(level dlog.LogLevel, args ...any) {
	lv := slogLevel(level)
	lg := clog.Logger(w)
	if lg.Enabled(w, lv) {
		lg.Log(w, lv, fmt.Sprintln(args...))
	}
}

func (w slogWrapper) WithField(key string, value any) dlog.Logger {
	ctx := w.Context
	if key == "THREAD" {
		// dlog assigns full names, whereas WithGroup append them so we can
		// only append the leaf here.
		ctx = clog.WithGroup(ctx, path.Base(fmt.Sprint(value)))
	} else {
		ctx = clog.With(w, key, value)
	}
	return dlog.BaseLogger{GenericLogger: slogWrapper{Context: ctx}}
}

type dlogWrapper struct{}

func (d dlogWrapper) Get(ctx context.Context) dlog.Logger {
	return dlog.BaseLogger{GenericLogger: slogWrapper{Context: ctx}}
}

func (d dlogWrapper) Set(ctx context.Context, w dlog.Logger) context.Context {
	lg1 := clog.Logger(ctx)
	lg2 := clog.Logger(w.(dlog.BaseLogger).GenericLogger.(slogWrapper))
	if lg1 != lg2 {
		ctx = clog.WithLogger(ctx, lg2)
	}
	return ctx
}

func Wrapper() dlog.Wrapper {
	return dlogWrapper{}
}
