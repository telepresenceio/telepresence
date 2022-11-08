package log

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type tbWrapper struct {
	testing.TB
	level  dlog.LogLevel
	fields map[string]any
}

type tbWriter struct {
	*tbWrapper
	l dlog.LogLevel
}

func (w *tbWriter) Write(data []byte) (n int, err error) {
	w.Helper()
	w.Log(w.l, strings.TrimSuffix(string(data), "\n")) // strip trailing newline if present, since the Log() call appends a newline
	return len(data), nil
}

func NewTestLogger(t testing.TB, level dlog.LogLevel) dlog.Logger {
	return &tbWrapper{TB: t, level: level}
}

func (w *tbWrapper) StdLogger(l dlog.LogLevel) *log.Logger {
	return log.New(&tbWriter{tbWrapper: w, l: l}, "", 0)
}

func (w *tbWrapper) WithField(key string, value any) dlog.Logger {
	ret := tbWrapper{
		TB:     w.TB,
		fields: make(map[string]any, len(w.fields)+1),
	}
	maps.Merge(ret.fields, w.fields)
	ret.fields[key] = value
	return &ret
}

func (w *tbWrapper) Log(level dlog.LogLevel, msg string) {
	if level > w.level {
		return
	}
	w.Helper()
	w.UnformattedLog(level, msg)
}

func (w *tbWrapper) UnformattedLog(level dlog.LogLevel, args ...any) {
	if level > w.level {
		return
	}
	w.Helper()
	sb := strings.Builder{}
	sb.WriteString(time.Now().Format("15:04:05.0000"))
	for _, arg := range args {
		sb.WriteString(" ")
		fmt.Fprint(&sb, arg)
	}

	if len(w.fields) > 0 {
		parts := make([]string, 0, len(w.fields))
		for k := range w.fields {
			parts = append(parts, k)
		}
		sort.Strings(parts)

		for i, k := range parts {
			if i > 0 {
				sb.WriteString(" ")
			}
			fmt.Fprintf(&sb, "%s=%#v", k, w.fields[k])
		}
	}
	w.TB.Log(sb.String())
}

func (w *tbWrapper) UnformattedLogf(level dlog.LogLevel, format string, args ...any) {
	if level > w.level {
		return
	}
	w.Helper()
	w.Log(level, fmt.Sprintf(format, args...))
}

func (w *tbWrapper) UnformattedLogln(level dlog.LogLevel, args ...any) {
	if level > w.level {
		return
	}
	w.Helper()
	w.Log(level, fmt.Sprintln(args...))
}
