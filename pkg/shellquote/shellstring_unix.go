//go:build !windows
// +build !windows

package shellquote

import (
	"io"
	"strings"
)

func quoteArg(arg string) string {
	return Unix(arg)
}

// Split the given string into an array, using shell quote semantics.
func Split(line string) ([]string, error) {
	if line == "" {
		return nil, nil
	}

	sb := strings.Builder{}
	parseDQSegment := func(s string) (string, int) {
		escaped := false
		for i, r := range s {
			if escaped {
				escaped = false
				switch r {
				case '"', '$', '\\':
					sb.WriteRune(r)
				// Skip escape character and write this one verbatim
				case '\n': // Escaped newline means concatenate the lines
				default:
					sb.WriteByte('\\') // Not known escape, so retain the escape character
					sb.WriteRune(r)
				}
			} else {
				if r == '"' {
					return sb.String(), i + 2
				}
				if r == '\\' {
					escaped = true
				} else {
					sb.WriteRune(r)
				}
			}
		}
		return "", -1
	}
	parseSQSegment := func(s string) (string, int) {
		for i, r := range s {
			if r == '\'' {
				return sb.String(), i + 2
			}
			sb.WriteRune(r)
		}
		return "", -1
	}

	parseUQSegment := func(s string) (string, int) {
		escaped := false
		for i, r := range s {
			if escaped {
				escaped = false
				switch r {
				case '\n': // Escaped newline means concatenate the lines
				default: // For all other cases, just skip the escape character and write the rune verbatim
					sb.WriteRune(r)
				}
			} else {
				switch r {
				case '"', '\'', ' ', '\t', '\r', '\n': // start of quoted string or whitespace ends this segment
					return sb.String(), i
				case '\\':
					escaped = true
				default:
					sb.WriteRune(r)
				}
			}
		}
		return sb.String(), len(s)
	}

	var ss []string
	e := -1
	newArg := true
	for i, r := range line {
		if i < e {
			continue
		}
		var s string
		var x int
		switch r {
		case ' ', '\t', '\r', '\n':
			// skip whitespace
			sb.Reset()
			newArg = true
			continue
		case '"':
			s, x = parseDQSegment(line[i+1:])
		case '\'':
			s, x = parseSQSegment(line[i+1:])
		default:
			s, x = parseUQSegment(line[i:])
		}
		if x < 0 {
			return nil, io.ErrUnexpectedEOF
		}
		e = i + x
		if newArg {
			ss = append(ss, s)
			newArg = false
		} else {
			ss[len(ss)-1] = s
		}
	}
	return ss, nil
}
