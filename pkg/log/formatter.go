package log

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

const thisModule = "github.com/telepresenceio/telepresence/v2"

// Formatter formats log messages for Telepresence.
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
	data := maps.Copy(entry.Data)
	goroutine, _ := data["THREAD"].(string)
	delete(data, "THREAD")

	if len(goroutine) > 0 {
		fmt.Fprintf(b, "%s %-*s %s : %s",
			entry.Time.Format(f.timestampFormat),
			len("warning"), entry.Level,
			strings.TrimPrefix(goroutine, "/"),
			entry.Message)
	} else {
		fmt.Fprintf(b, "%s %-*s %s",
			entry.Time.Format(f.timestampFormat),
			len("warning"), entry.Level,
			entry.Message)
	}

	if len(data) > 0 {
		b.WriteString(" :")
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			orders := map[string]int{
				"dexec.pid":    -4,
				"dexec.stream": -3,
				"dexec.data":   -2,
				"dexec.err":    -1,
			}
			iOrd := orders[keys[i]]
			jOrd := orders[keys[j]]
			if iOrd != jOrd {
				return iOrd < jOrd
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			val := fmt.Sprintf("%+v", data[key])
			fmt.Fprintf(b, " %s=%q", key, val)
		}
	}

	if entry.HasCaller() && strings.HasPrefix(entry.Caller.File, thisModule+"/") {
		fmt.Fprintf(b, " (from %s:%d)", strings.TrimPrefix(entry.Caller.File, thisModule+"/"), entry.Caller.Line)
	}

	b.WriteByte('\n')

	return b.Bytes(), nil
}
