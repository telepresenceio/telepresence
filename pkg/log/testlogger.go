package log

import (
	"fmt"
	"io"
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

func NewTestLogger(t testing.TB, level dlog.LogLevel) dlog.Logger {
	return &tbWrapper{TB: t, level: level}
}

func (w *tbWrapper) StdLogger(_ dlog.LogLevel) *log.Logger {
	return log.New(io.Discard, "", 0)
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

func (w *tbWrapper) Log(level dlog.LogLevel, msg string) {
	if level > w.level {
		return
	}
	w.Helper()
	sb := strings.Builder{}
	sb.WriteString(time.Now().Format("15:04:05.0000"))
	sb.WriteString(" ")
	sb.WriteString(msg)

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
