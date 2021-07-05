package logging

import (
	"strings"
)

func ShellString(exe string, args []string) string {
	b := strings.Builder{}
	b.WriteString(quoteArg(exe))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

func ShellArgsString(args []string) string {
	b := strings.Builder{}
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(a))
	}
	return b.String()
}
