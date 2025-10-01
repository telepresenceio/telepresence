package matcher

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// The Request matcher uses a Value matcher and a Headers matcher to match the path and headers of a http request.
type Request interface {
	fmt.Stringer

	// Headers returns Headers of this instance.
	Headers() Headers

	// IsGlobal returns true if this instance matches all requests.
	IsGlobal() bool

	// Map returns the map correspondence of this instance. The returned value can be
	// used as an argument to NewRequest to create an identical Request.
	Map() map[string]string

	// Matches returns true if given http.Request is matched.
	Matches(req *http.Request) bool

	// MatchesPathAndHeader returns true if both the path Headers match.
	MatchesPathAndHeader(path string, req http.Header) bool

	// Paths return the path matchers.
	Paths() Paths
}

type request struct {
	paths   Paths
	headers HeaderMap
}

// NewRequestFromMap creates a new Request based on the values of the given map. Aside from http headers,
// the map may contain a :path: entry with a semicolon delimited list of paths. Each path should be prefixed
// with one of three special keys.
//
//	:path-equal: path will match if equal to the value
//	:path-prefix: path will match prefixed by the value
//	:path-regex: path will match it matches the regexp value
func NewRequestFromMap(m map[string]string) Request {
	if len(m) == 0 {
		return &request{}
	}
	var ps Paths
	var hm HeaderMap
	for k, v := range m {
		switch k {
		case ":paths:":
			ps = NewPaths(strings.Split(v, ";"))
		default:
			vm := NewValue(v)
			if hm == nil {
				hm = make(HeaderMap)
			}
			hm[textproto.CanonicalMIMEHeaderKey(k)] = vm
		}
	}
	return &request{paths: ps, headers: hm}
}

func NewRequest(paths []string, hdrs map[string]string) Request {
	rv := new(request)
	if len(paths) > 0 {
		rv.paths = NewPaths(paths)
	}
	if len(hdrs) > 0 {
		rv.headers = NewHeaders(hdrs).HeaderMap()
	}
	return rv
}

// Map returns the map correspondence of this instance. The returned value can be
// used as an argument to NewRequest to create an identical Request.
func (r *request) Map() map[string]string {
	var m map[string]string
	if len(r.headers) > 0 {
		m = r.headers.Map()
	}
	if len(r.paths) > 0 {
		pm := map[string]string{":paths:": strings.Join(r.paths.Slice(), ";")}
		maps.Merge(pm, m)
		m = pm
	}
	return m
}

// Headers returns Headers of this instance.
func (r *request) Headers() Headers {
	return r.headers
}

func (r *request) IsGlobal() bool {
	return len(r.paths) == 0 && len(r.headers) == 0
}

// Matches returns true if both the path Value matcher and the Headers matcher in this instance are
// matched by the given http.Request.
func (r *request) Matches(req *http.Request) bool {
	return r.MatchesPathAndHeader(req.URL.Path, req.Header)
}

func (r *request) MatchesPathAndHeader(path string, header http.Header) bool {
	return r.paths.Matches(path) && (len(r.headers) == 0 || r.headers.Matches(header))
}

// Paths return the path matchers.
func (r *request) Paths() Paths {
	return r.paths
}

func (r *request) String() string {
	if r.IsGlobal() {
		return "all TCP connections"
	}
	sb := strings.Builder{}
	sb.WriteString("HTTP requests with")
	switch len(r.paths) {
	case 0:
		sb.WriteByte(' ')
		r.headers.appendString(&sb, "")
	case 1:
		if len(r.headers) < 2 {
			sb.WriteByte(' ')
			r.paths.appendString(&sb, "")
			if len(r.headers) > 0 {
				sb.WriteString(" and ")
				r.headers.appendString(&sb, "")
			}
			break
		}
		fallthrough
	default:
		if len(r.headers) > 0 {
			sb.WriteByte('\n')
			r.paths.appendString(&sb, " ")
			sb.WriteByte('\n')
			r.headers.appendString(&sb, " ")
		} else {
			sb.WriteByte(' ')
			r.paths.appendString(&sb, "")
		}
	}
	return sb.String()
}
