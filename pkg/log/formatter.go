package log

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

const thisModule = "github.com/telepresenceio/telepresence/v2"

// formatter implements logrus.Formatter.
type formatter struct {
	timestampFormat string
	levelThreshold  logrus.Level
}

type FormatterOption func(*formatter)

// WithTimestampFormat will cause log messages to include a timestamp formatted according to the given format.
// Unless given, or if the format is an empty string, messages will be logged without a timestamp.
func WithTimestampFormat(format string) FormatterOption {
	return func(f *formatter) { f.timestampFormat = format }
}

// WithLevelPrefixThreshold will cause log messages with a level less than this threshold to be
// logged without a level prefix.
func WithLevelPrefixThreshold(level logrus.Level) FormatterOption {
	return func(f *formatter) { f.levelThreshold = level }
}

func NewFormatter(options ...FormatterOption) logrus.Formatter {
	f := &formatter{}
	for _, opt := range options {
		opt(f)
	}
	return f
}

func padLevel(b *bytes.Buffer, level logrus.Level) {
	lvl := level.String()
	b.WriteString(lvl)
	for i := len(lvl); i < 8; i++ {
		b.WriteByte(' ')
	}
}

// Format implements logrus.Formatter.
func (f *formatter) Format(entry *logrus.Entry) ([]byte, error) {
	b := entry.Buffer
	if b == nil {
		b = &bytes.Buffer{}
	}

	if f.timestampFormat != "" {
		b.WriteString(entry.Time.Format(f.timestampFormat))
		b.WriteByte(' ')
	}
	if entry.Level >= f.levelThreshold {
		padLevel(b, entry.Level)
	}

	data := entry.Data
	dataLen := len(data)
	if thread, ok := data["THREAD"]; ok {
		dataLen--
		if goroutine, ok := thread.(string); ok && goroutine != "" {
			b.WriteString(strings.TrimPrefix(goroutine, "/"))
			b.WriteString(" : ")
		}
	}
	b.WriteString(entry.Message)

	if dataLen > 0 {
		b.WriteString(" :")
		keys := make([]string, dataLen)
		i := 0
		for key := range data {
			if key != "THREAD" {
				keys[i] = key
				i++
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			ioutil.Printf(b, " %s=%q", key, fmt.Sprintf("%+v", data[key]))
		}
	}

	if entry.HasCaller() && strings.HasPrefix(entry.Caller.File, thisModule) {
		ioutil.Printf(b, " (from %s:%d)", strings.TrimPrefix(entry.Caller.File, thisModule), entry.Caller.Line)
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
