package matcher

import (
	"slices"
	"strings"
)

type Paths []Value

const (
	PathEqual  = ":path-equal:"
	PathPrefix = ":path-prefix:"
	PathRegex  = ":path-regex:"
)

func splitPath(path string) (string, string) {
	if len(path) > 0 && path[0] == ':' {
		nxt := strings.Index(path[1:], ":")
		if nxt > 0 {
			nxt += 2
			return path[:nxt], path[nxt:]
		}
	}
	return PathEqual, path
}

func PathValue(path string) (pv Value) {
	p, v := splitPath(path)
	switch p {
	case PathPrefix:
		pv = NewPrefix(v)
	case PathRegex:
		pv = NewRegex(v)
	default:
		pv = NewEqual(v)
	}
	return pv
}

func NewPaths(paths []string) Paths {
	pvs := make(Paths, len(paths))
	for i, pv := range paths {
		pvs[i] = PathValue(pv)
	}
	return pvs
}

func (ps Paths) Slice() []string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		var pfx string
		switch p.Op() {
		case ValueOpRegex:
			pfx = PathRegex
		case ValueOpPrefix:
			pfx = PathPrefix
		default:
			pfx = PathEqual
		}
		ss[i] = pfx + p.String()
	}
	return ss
}

// Matches returns true if at least one of the paths in this instance matches the given path
// or if this instance is empty.
func (ps Paths) Matches(path string) bool {
	return len(ps) == 0 || slices.ContainsFunc(ps, func(v Value) bool { return v.Matches(path) })
}

func (ps Paths) String() string {
	sb := strings.Builder{}
	ps.appendString(&sb, "")
	return sb.String()
}

func (ps Paths) appendString(sb *strings.Builder, indent string) {
	if len(ps) == 0 {
		return
	}
	sb.WriteString(indent)
	sb.WriteString("path")
	if len(ps) > 1 {
		sb.WriteString("s\n")
		indent += " "
	} else {
		sb.WriteByte(' ')
		indent = ""
	}
	for i, p := range ps {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(indent)
		sb.WriteString(string(p.Op()))
		sb.WriteByte(' ')
		sb.WriteString(p.String())
	}
}
