package matcher

import (
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// HeaderMember splits a header filter name of the form "name[key]" into the header name and the key of
// a member in that header's value. The filter "baggage[session]" with value "alice-1" matches a request
// with the header "baggage: tenant=acme,session=alice-1".
//
// The header value is read as a comma-separated list (RFC 9110, section 5.6.1) whose members have the
// form key=value, optionally followed by ';'-separated parameters, as in the baggage, tracestate, and
// forwarded headers. The cookie header is parsed as cookies instead.
func HeaderMember(filterName string) (name, key string, ok bool) {
	if !strings.HasSuffix(filterName, "]") {
		return "", "", false
	}
	name, key, ok = strings.Cut(filterName[:len(filterName)-1], "[")
	if !ok || !httpguts.ValidHeaderFieldName(name) || key == "" || strings.ContainsAny(key, "[]") {
		return "", "", false
	}
	return name, key, true
}

// HasHeaderMember returns true if any of the given header filter names has the form "name[key]".
func HasHeaderMember(headerFilters map[string]string) bool {
	for k := range headerFilters {
		if _, _, ok := HeaderMember(k); ok {
			return true
		}
	}
	return false
}

// canonicalHeaderKey returns the textproto.CanonicalMIMEHeaderKey form of the given filter name. Only
// the header name of a "name[key]" filter is canonicalized. The member key is case-sensitive.
func canonicalHeaderKey(filterName string) string {
	if name, key, ok := HeaderMember(filterName); ok {
		return textproto.CanonicalMIMEHeaderKey(name) + "[" + key + "]"
	}
	return textproto.CanonicalMIMEHeaderKey(filterName)
}

// headerValue returns the value that the filter with the given name is matched against, and false
// when the filter targets a member that the request lacks. The first member with the key wins.
func headerValue(h http.Header, filterName string) (string, bool) {
	name, key, ok := HeaderMember(filterName)
	if !ok {
		return h.Get(filterName), true
	}
	if textproto.CanonicalMIMEHeaderKey(name) == "Cookie" {
		c, err := (&http.Request{Header: h}).Cookie(key)
		if err != nil {
			return "", false
		}
		return percentDecode(c.Value), true
	}
	for _, line := range h.Values(name) {
		for line != "" {
			var member string
			member, line = cutUnquoted(line, ',')
			member, _ = cutUnquoted(member, ';')
			if k, v, ok := strings.Cut(member, "="); ok && strings.TrimSpace(k) == key {
				return percentDecode(unquote(strings.TrimSpace(v))), true
			}
		}
	}
	return "", false
}

// cutUnquoted slices s around the first sep that is outside a double-quoted string.
func cutUnquoted(s string, sep byte) (before, after string) {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case !quoted && c == sep:
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// unquote returns the content of a double-quoted string with its quoted-pairs resolved, or s
// unchanged when it isn't quoted.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	if !strings.Contains(s, `\`) {
		return s
	}
	sb := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// percentDecode returns the percent-decoded form of v, or v unchanged when it fails to decode.
func percentDecode(v string) string {
	if dv, err := url.PathUnescape(v); err == nil {
		return dv
	}
	return v
}
