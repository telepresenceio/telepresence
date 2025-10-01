package matcher

import (
	"net/http"
	"net/textproto"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type HeaderMap map[string]Value

// Headers uses a set of Value matchers to match a http.Header.
type Headers interface {
	// Map returns the map correspondence of this instance. The returned value can be
	// used as an argument to NewHeaders to create an identical Headers.
	Map() map[string]string

	// HeaderMap returns HeaderMap correspondence of this instance.
	HeaderMap() HeaderMap

	// Matches returns true if all Value matchers in this instance are matched by the given http.Header.
	// Header name comparison is made using the textproto.CanonicalMIMEHeaderKey form of the keys.
	Matches(header http.Header) bool
}

// NewHeaders creates a new Headers with all header keys normalized to canonical MIME format.
// HTTP headers are case-insensitive per RFC 7230, so we use the same canonicalization as net/http.
//
// Examples: "x-user" -> "X-User", "content-type" -> "Content-Type".
func NewHeaders(hs map[string]string) Headers {
	hm := make(HeaderMap, len(hs))
	for k, v := range hs {
		hm[textproto.CanonicalMIMEHeaderKey(k)] = NewValue(v)
	}
	return hm
}

// Map returns the map correspondence of this instance. The returned value can be
// used as an argument to NewHeaders to create an identical Headers.
func (m HeaderMap) Map() map[string]string {
	r := make(map[string]string, len(m))
	for k, v := range m {
		r[k] = v.String()
	}
	return r
}

// HeaderMap returns the internal HeaderMap. Any modifications made to this map must
// ensure that keys are canonicalized using textproto.CanonicalMIMEHeaderKey.
func (m HeaderMap) HeaderMap() HeaderMap {
	return m
}

// Matches returns true if all Value matchers in this instance are matched by the given http.Header.
// Header name comparison is made using the textproto.CanonicalMIMEHeaderKey form of the keys.
func (m HeaderMap) Matches(h http.Header) bool {
	for name, vm := range m {
		if v := h.Get(name); !vm.Matches(v) {
			return false
		}
	}
	return true
}

func (m HeaderMap) String() string {
	sb := strings.Builder{}
	m.appendString(&sb, "")
	return sb.String()
}

func (m HeaderMap) appendString(sb *strings.Builder, indent string) {
	if len(m) == 0 {
		return
	}
	sb.WriteString(indent)
	sb.WriteString("header")
	if len(m) > 1 {
		sb.WriteString("s\n")
		indent += " "
	} else {
		sb.WriteByte(' ')
		indent = ""
	}
	first := true
	for _, k := range maps.SortedKeys(m) {
		if !first {
			sb.WriteByte('\n')
		}
		first = false
		v := m[k]
		op := v.Op()
		if op == "==" {
			ioutil.Printf(sb, "%s'%s: %s'", indent, k, v)
		} else {
			ioutil.Printf(sb, "%s'%s %s %s'", indent, k, v.Op(), v)
		}
	}
}
