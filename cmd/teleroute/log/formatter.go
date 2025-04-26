package log

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/sirupsen/logrus"
)

// Formatter formats log messages for the Teleroute plugin.
type Formatter struct {
	timestampFormat string
}

func NewFormatter(timestampFormat string) *Formatter {
	return &Formatter{timestampFormat: timestampFormat}
}

// Format implements logrus.Formatter.
func (f *Formatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}
	_, _ = fmt.Fprintf(b, "%s %-7s %s", entry.Time.Format(f.timestampFormat), entry.Level, entry.Message)
	ed := entry.Data
	if el := len(ed); el > 0 {
		b.WriteString(" :")
		keys := make([]string, el)
		i := 0
		for key := range ed {
			keys[i] = key
			i++
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, _ = fmt.Fprintf(b, " %s=%q", key, fmt.Sprintf("%+v", ed[key]))
		}
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
