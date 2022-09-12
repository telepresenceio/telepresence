package matcher

import (
	"net/http"
	"sort"
	"strings"
)

// HeaderStringer turns a http.Header into a fmt.Stringer. It is useful when it's desired to defer string formatting of
// the header depending on loglevel, for instance:
//
//	dlog.Debugf(c, "Header = %s", HeaderStringer(header))
//
// would not perform the actual formatting unless the loglevel is DEBUG or higher.
type HeaderStringer http.Header

// String formats the Header to a readable multi-line string.
func (s HeaderStringer) String() string {
	h := http.Header(s)
	sb := strings.Builder{}
	ks := make([]string, len(h))
	i := 0
	for k := range h {
		ks[i] = k
		i++
	}
	sort.Strings(ks)
	for i, k := range ks {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(k)
		sb.WriteString(": ")
		for p, v := range h[k] {
			if p > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(v)
		}
	}
	return sb.String()
}
