package logging

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

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
	b.WriteString(entry.Time.Format(f.timestampFormat))
	b.WriteByte(' ')

	var keys []string
	if len(entry.Data) > 0 {
		keys = make([]string, 0, len(entry.Data))
		for k, v := range entry.Data {
			if k == "THREAD" {
				tn := v.(string)
				tn = strings.TrimPrefix(tn, "/")
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

func ShellString(exe string, args []string) string {
	b := strings.Builder{}
	b.WriteString(quoteArg(exe))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

var escape = regexp.MustCompile(`[^\w!%+,\-./:=@^]`)

// quoteArg checks if the give string contains characters that have special meaning for a
// shell. If it does, it will be quoted using single quotes. If the string itself contains
// single quotes, then the string is split on single quotes, each single quote is escaped
// and each segment between the escaped single quotes is quoted separately.
func quoteArg(arg string) string {
	if arg == "" {
		return `''`
	}
	if !escape.MatchString(arg) {
		return arg
	}

	b := strings.Builder{}
	qp := strings.IndexByte(arg, '\'')
	if qp < 0 {
		b.WriteByte('\'')
		b.WriteString(arg)
		b.WriteByte('\'')
	} else {
		for {
			if qp > 0 {
				// Write quoted string up to qp
				b.WriteString(quoteArg(arg[:qp]))
			}
			b.WriteString(`\'`)
			qp++
			if qp >= len(arg) {
				break
			}
			arg = arg[qp:]
			if qp = strings.IndexByte(arg, '\''); qp < 0 {
				if len(arg) > 0 {
					b.WriteString(quoteArg(arg))
				}
				break
			}
		}
	}
	return b.String()
}
