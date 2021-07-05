// +build !windows

package logging

import (
	"regexp"
	"strings"
)

var escape = regexp.MustCompile(`[^\w!%+,\-./:=@^']`)

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
