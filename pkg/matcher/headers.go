package matcher

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

// Headers uses a set of Value matchers to match a http.Header
type Headers interface {
	// Map returns the map correspondence of this instance. The returned value can be
	// used as an argument to NewHeaders to create an identical Headers.
	Map() map[string]string

	// Matches returns true if all Value matchers in this instance are matched by the given http.Header.
	// Header name comparison is made using the textproto.CanonicalMIMEHeaderKey form of the keys.
	Matches(header http.Header) bool
}

type headers map[string]Value

// NewHeaders creates a new Headers.
func NewHeaders(hs map[string]string) (Headers, error) {
	hm := make(headers, len(hs))
	for k, v := range hs {
		vm, err := NewValue(v)
		if err != nil {
			return nil, fmt.Errorf("the value of match %s=%s is invalid: %w", k, v, err)
		}
		hm[textproto.CanonicalMIMEHeaderKey(k)] = vm
	}
	return hm, nil
}

// Map returns the map correspondence of this instance. The returned value can be
// used as an argument to NewHeaders to create an identical Headers.
func (m headers) Map() map[string]string {
	r := make(map[string]string, len(m))
	for k, v := range m {
		r[k] = v.String()
	}
	return r
}

// Matches returns true if all Value matchers in this instance are matched by the given http.Header.
// Header name comparison is made using the textproto.CanonicalMIMEHeaderKey form of the keys.
func (m headers) Matches(h http.Header) bool {
	for name, vm := range m {
		if v := h.Get(name); !vm.Matches(v) {
			return false
		}
	}
	return true
}

func (m headers) String() string {
	sb := strings.Builder{}
	m.appendString(&sb, "")
	return sb.String()
}

func (m headers) appendString(sb *strings.Builder, indent string) {
	for k, v := range m {
		fmt.Fprintf(sb, "\n%s%s %s %s", indent, k, v.Op(), v)
	}
}
