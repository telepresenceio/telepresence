package matcher

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

// The Request matcher uses a Value matcher and a Headers matcher to match the path and headers of a http request
type Request interface {
	fmt.Stringer

	// Headers returns Headers of this instance.
	Headers() Headers

	// Map returns the map correspondence of this instance. The returned value can be
	// used as an argument to NewRequest to create an identical Request.
	Map() map[string]string

	// Matches returns true if both the path Value matcher and the Headers matcher in this instance are
	// matched by the given http.Request.
	Matches(path string, headers http.Header) bool

	// Path returns the path
	Path() Value
}

type request struct {
	path    Value
	headers HeaderMap
}

// NewRequestFromMap creates a new Request based on the values of the given map. Aside from http headers,
// the map may contain one of three special keys.
//
//   :path-equal: path will match if equal to the value
//   :path-prefix: path will match prefixed by the value
//   :path-regex: path will match it matches the regexp value
//
func NewRequestFromMap(m map[string]string) (Request, error) {
	var pm Value
	hm := make(HeaderMap, len(m))

	var err error
	for k, v := range m {
		switch k {
		case ":path-equal:":
			pm = NewEqual(v)
		case ":path-prefix:":
			pm = NewPrefix(v)
		case ":path-regex:":
			if pm, err = NewRegex(v); err != nil {
				return nil, err
			}
		default:
			vm, err := NewValue(v)
			if err != nil {
				return nil, fmt.Errorf("the value of match %s=%s is invalid: %w", k, v, err)
			}
			hm[textproto.CanonicalMIMEHeaderKey(k)] = vm
		}
	}
	return NewRequest(pm, hm), nil
}

func NewRequest(path Value, hm HeaderMap) Request {
	if len(hm) == 0 {
		hm = nil
	}
	return &request{path: path, headers: hm}
}

// Map returns the map correspondence of this instance. The returned value can be
// used as an argument to NewRequest to create an identical Request.
func (r *request) Map() map[string]string {
	var m map[string]string
	if r.headers != nil {
		m = r.headers.Map()
	}
	if p := r.path; p != nil {
		pm := make(map[string]string, len(m)+1)
		switch p.(type) {
		case textValue:
			pm[":path-equal:"] = p.String()
		case prefixValue:
			pm[":path-prefix:"] = p.String()
		case rxValue:
			pm[":path-regex:"] = p.String()
		}
		for k, v := range m {
			pm[k] = v
		}
		m = pm
	}
	return m
}

// Headers returns Headers of this instance.
func (r *request) Headers() Headers {
	return r.headers
}

// Matches returns true if both the path Value matcher and the Headers matcher in this instance are
// matched by the given http.Request.
func (r *request) Matches(path string, headers http.Header) bool {
	return r == nil || (r.path == nil || r.path.Matches(path)) && (r.headers == nil || r.headers.Matches(headers))
}

// Path returns the path
func (r *request) Path() Value {
	return r.path
}

func (r *request) String() string {
	sb := strings.Builder{}
	if r == nil || r.path == nil && len(r.headers) == 0 {
		return "all requests"
	}
	sb.WriteString("requests with")
	if r.path != nil {
		if r.headers != nil {
			sb.WriteString("\n ")
		}
		fmt.Fprintf(&sb, " path %s %s", r.path.Op(), r.path.String())
	}
	if r.headers != nil {
		indent := "  "
		if r.path != nil {
			indent += "  "
			sb.WriteString("\n ")
		}
		sb.WriteString(" headers")
		r.headers.appendString(&sb, indent)
	}
	return sb.String()
}
