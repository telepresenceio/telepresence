package logging

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

const thisModule = "github.com/telepresenceio/telepresence/v2"

// Formatter formats log messages for Telepresence client
type Formatter struct {
	timestampFormat string
}

func NewFormatter(timestampFormat string) *Formatter {
	return &Formatter{timestampFormat: timestampFormat}
}

// Format implements logrus.Formatter
func (f *Formatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}
	data := make(logrus.Fields, len(entry.Data))
	for k, v := range entry.Data {
		data[k] = v
	}
	goroutine, _ := data["THREAD"].(string)
	delete(data, "THREAD")

	fmt.Fprintf(b, "%s %-*s %s : %s",
		entry.Time.Format(f.timestampFormat),
		len("warning"), entry.Level,
		strings.TrimPrefix(goroutine, "/"),
		entry.Message)

	if len(data) > 0 {
		b.WriteString(" :")
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			val := data[key]
			fmt.Fprintf(b, " %s=%+v", key, val)
		}
		b.WriteByte(')')
	}

	if entry.HasCaller() && strings.HasPrefix(entry.Caller.File, thisModule+"/") {
		fmt.Fprintf(b, " (from %s:%d)", strings.TrimPrefix(entry.Caller.File, thisModule+"/"), entry.Caller.Line)
	}

	b.WriteByte('\n')

	return b.Bytes(), nil
}
