package client

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

// LogFormatter formats log messages for Telepresence client
type LogFormatter struct {
	timestampFormat string
}

func NewFormatter(timestampFormat string) *LogFormatter {
	return &LogFormatter{timestampFormat: timestampFormat}
}

// Format implements logrus.Formatter
func (f *LogFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}
	b.WriteString(entry.Time.Format(f.timestampFormat))
	b.WriteByte(' ')

	var keys []string
	if len(entry.Data) > 0 {
		keys = make([]string, 0, len(entry.Data))
		for k, v := range entry.Data {
			if k == "THREAD" {
				tn := v.(string)
				if strings.HasPrefix(tn, "/") {
					tn = tn[1:]
				}
				b.WriteString(tn)
				b.WriteByte(' ')
			} else {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
	}

	b.WriteString(entry.Message)
	for _, k := range keys {
		v := entry.Data[k]
		fmt.Fprintf(b, " %s=%+v", k, v)
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
