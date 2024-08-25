package shellquote

import (
	"regexp"
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

var UnixEscape = regexp.MustCompile(`[^\w!%+,\-./:=@^]`)

// Unix checks if the give string contains characters that have special meaning for a
// shell. If it does, it will be quoted using single quotes. If the string itself contains
// single quotes, then the string is split on single quotes, each single quote is escaped
// and each segment between the escaped single quotes is quoted separately.
func Unix(arg string) string {
	if arg == "" {
		return `''`
	}
	if !UnixEscape.MatchString(arg) {
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
				b.WriteString(Unix(arg[:qp]))
			}
			b.WriteString(`\'`)
			qp++
			if qp >= len(arg) {
				break
			}
			arg = arg[qp:]
			if qp = strings.IndexByte(arg, '\''); qp < 0 {
				if len(arg) > 0 {
					b.WriteString(Unix(arg))
				}
				break
			}
		}
	}
	return b.String()
}

func Windows(arg string) string {
	if arg == "" {
		return `""`
	}
	needsBackslash := false
	needsQuote := false
	for _, c := range arg {
		switch c {
		case '"', '\\':
			needsBackslash = true
		case ' ', '\t':
			needsQuote = true
		}
	}
	if !(needsBackslash || needsQuote) {
		return arg
	}

	b := strings.Builder{}
	slashes := 0
	slashOut := func() {
		for ; slashes > 0; slashes-- {
			b.WriteByte('\\')
		}
	}
	slashOutBeforeQuote := func(escape bool) {
		slashes <<= 1
		if escape {
			slashes++
		}
		slashOut()
	}

	if needsQuote {
		b.WriteByte('"')
	}

	if !needsBackslash {
		b.WriteString(arg)
		b.WriteByte('"')
		return b.String()
	}

	for _, c := range arg {
		switch c {
		default:
			slashOut()
			b.WriteRune(c)
		case '\\':
			slashes++
		case '"':
			slashOutBeforeQuote(true)
			b.WriteByte('"')
		}
	}
	if needsQuote {
		slashOutBeforeQuote(false)
		b.WriteByte('"')
	} else {
		slashOut()
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
