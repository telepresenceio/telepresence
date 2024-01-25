package ioutil

import (
	"io"
	"strings"
)

// KeyValueFormatter will format each key/value pair added by Add so that they
// are prefixed with Prefix and have a vertically aligned ':' between the key and
// the value. Each pair is separated by a newline, and if a value contains newlines,
// then each line in that value, except the first one will be prefixed with `Prefix`,
// and Indent.
type KeyValueFormatter struct {
	kvs       []string
	Prefix    string
	Indent    string
	Separator string
}

type KeyValueProvider interface {
	AddTo(*KeyValueFormatter)
}

func DefaultKeyValueFormatter() *KeyValueFormatter {
	return &KeyValueFormatter{
		Indent:    "    ",
		Separator: ": ",
	}
}

// Add adds a key value pair that will be included in the formatted output.
func (f *KeyValueFormatter) Add(k, v string) {
	f.kvs = append(f.kvs, k, v)
}

// WriteTo writes the formatted output to the given io.Writer.
func (f *KeyValueFormatter) WriteTo(out io.Writer) (int64, error) {
	kLen := 0
	kvs := f.kvs
	t := len(kvs)

	// Figure out length of the longest key
	for i := 0; i < t; i += 2 {
		if l := len(kvs[i]); l > kLen {
			kLen = l
		}
	}
	n := 0
	for i := 0; i < t; i += 2 {
		if i > 0 {
			n += WriteString(out, "\n")
		}
		lines := strings.Split(strings.TrimRight(kvs[i+1], " \t\r\n"), "\n")
		n += Printf(out, "%s%-*s%s%s", f.Prefix, kLen, kvs[i], f.Separator, lines[0])
		for _, line := range lines[1:] {
			n += Printf(out, "\n%s%s%s", f.Prefix, f.Indent, line)
		}
	}
	return int64(n), nil
}

func (f *KeyValueFormatter) Println(out io.Writer) int {
	n, _ := f.WriteTo(out)
	return int(n) + Println(out, "")
}

// String returns the formatted output string.
func (f *KeyValueFormatter) String() string {
	sb := &strings.Builder{}
	_, _ = f.WriteTo(sb)
	return sb.String()
}
