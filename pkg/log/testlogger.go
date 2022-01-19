package log

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
)

type tbWrapper struct {
	testing.TB
	level  dlog.LogLevel
	fields map[string]interface{}
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

func (w *tbWrapper) WithField(key string, value interface{}) dlog.Logger {
	ret := tbWrapper{
		TB:     w.TB,
		fields: make(map[string]interface{}, len(w.fields)+1),
	}
	for k, v := range w.fields {
		ret.fields[k] = v
	}
	ret.fields[key] = value
	return &ret
}

func (w *tbWrapper) Log(level dlog.LogLevel, args ...interface{}) {
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

func (w *tbWrapper) Logf(level dlog.LogLevel, format string, args ...interface{}) {
	w.Log(level, fmt.Sprintf(format, args...))
}

func (w *tbWrapper) Logln(level dlog.LogLevel, args ...interface{}) {
	w.Log(level, fmt.Sprintln(args...))
}
