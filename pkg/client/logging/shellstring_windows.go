package logging

import (
	"strings"
)

// quoteArg is heavy influenced by appendEscapeArg() in exec_windows.go in the syscall package
func quoteArg(arg string) string {
	if arg == "" {
		return `""`
	}
	needsBackslash := false
	hasSpace := false
	for _, c := range arg {
		switch c {
		case '"', '\\':
			needsBackslash = true
		case ' ', '\t':
			hasSpace = true
		}
	}

	if !needsBackslash && !hasSpace {
		// No special handling required; normal case.
		return arg
	}

	b := strings.Builder{}
	if !needsBackslash {
		// hasSpace is true, so we need to quote the string.
		b.WriteByte('"')
		b.WriteString(arg)
		b.WriteByte('"')
		return b.String()
	}

	if hasSpace {
		b.WriteByte('"')
	}
	slashes := 0
	for _, c := range arg {
		switch c {
		default:
			slashes = 0
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				b.WriteByte('\\')
			}
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	if hasSpace {
		for ; slashes > 0; slashes-- {
			b.WriteByte('\\')
		}
		b.WriteByte('"')
	}
	return b.String()
}
