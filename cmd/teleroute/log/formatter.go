package log

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/sirupsen/logrus"
)

const FieldNetworkID = "networkID"

// Formatter formats log messages for the Teleroute plugin.
type Formatter struct {
	timestampFormat string
}

func NetworkLogger(networkID string) logrus.FieldLogger {
	return logrus.WithField(FieldNetworkID, networkID[:12])
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
	ed := entry.Data
	exl := 0
	if nid, ok := ed[FieldNetworkID].(string); ok {
		_, _ = fmt.Fprintf(b, "%s %-7s %s: %s", entry.Time.Format(f.timestampFormat), entry.Level, nid, entry.Message)
		exl++
	} else {
		_, _ = fmt.Fprintf(b, "%s %-7s: %s", entry.Time.Format(f.timestampFormat), entry.Level, entry.Message)
	}
	if len(ed) > exl {
		keys := make([]string, len(ed)-exl)
		i := 0
		for key := range ed {
			if key != FieldNetworkID {
				keys[i] = key
				i++
			}
		}
		sort.Strings(keys)
		b.WriteString(" :")
		for _, key := range keys {
			_, _ = fmt.Fprintf(b, " %s=%q", key, fmt.Sprintf("%+v", ed[key]))
		}
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
