package matcher

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeaderMember(t *testing.T) {
	tests := []struct {
		filterName string
		name       string
		key        string
		ok         bool
	}{
		{"baggage[session]", "baggage", "session", true},
		{"Cookie[sid]", "Cookie", "sid", true},
		{"baggage", "", "", false},
		{"baggage[]", "", "", false},
		{"[session]", "", "", false},
		{"baggage[session", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.filterName, func(t *testing.T) {
			name, key, ok := HeaderMember(tt.filterName)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.name, name)
			assert.Equal(t, tt.key, key)
		})
	}
}

func Test_headers_MatchesMember(t *testing.T) {
	tests := []struct {
		name   string
		match  map[string]string
		header http.Header
		want   bool
	}{
		{
			name:   "only member",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"session=alice-1"}},
			want:   true,
		},
		{
			name:   "member among others",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme,session=alice-1,region=eu"}},
			want:   true,
		},
		{
			name:   "whitespace around members",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme , session = alice-1 ,region=eu"}},
			want:   true,
		},
		{
			name:   "member with properties",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme,session=alice-1;ttl=60;sampled"}},
			want:   true,
		},
		{
			name:   "member in second header line",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme", "session=alice-1"}},
			want:   true,
		},
		{
			name:   "header name is case-insensitive",
			match:  map[string]string{"BAGGAGE[session]": "alice-1"},
			header: http.Header{"Baggage": {"session=alice-1"}},
			want:   true,
		},
		{
			name:   "member key is case-sensitive",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"Session=alice-1"}},
			want:   false,
		},
		{
			name:   "member before a property with the same key",
			match:  map[string]string{"baggage[session]": "bob-2"},
			header: http.Header{"Baggage": {"tenant=acme;session=alice-1,session=bob-2"}},
			want:   true,
		},
		{
			name:   "property with the same key as a member is not the member",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme;session=alice-1,session=bob-2"}},
			want:   false,
		},
		{
			name:   "percent-encoded value",
			match:  map[string]string{"baggage[session]": "alice@example.com"},
			header: http.Header{"Baggage": {"tenant=acme,session=alice%40example.com"}},
			want:   true,
		},
		{
			name:   "plus kept literally",
			match:  map[string]string{"baggage[session]": `^alice\+1$`},
			header: http.Header{"Baggage": {"session=alice+1"}},
			want:   true,
		},
		{
			name:   "invalid percent-encoding matched raw",
			match:  map[string]string{"baggage[session]": "alice%zz"},
			header: http.Header{"Baggage": {"session=alice%zz"}},
			want:   true,
		},
		{
			name:   "other value",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme,session=bob-2"}},
			want:   false,
		},
		{
			name:   "value is a prefix of the member value",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"session=alice-10"}},
			want:   false,
		},
		{
			name:   "value of another member",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"user=alice-1"}},
			want:   false,
		},
		{
			name:   "missing member",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{"Baggage": {"tenant=acme"}},
			want:   false,
		},
		{
			name:   "missing header",
			match:  map[string]string{"baggage[session]": "alice-1"},
			header: http.Header{},
			want:   false,
		},
		{
			name:   "empty value matches an empty member",
			match:  map[string]string{"baggage[session]": ""},
			header: http.Header{"Baggage": {"session="}},
			want:   true,
		},
		{
			name:   "empty value does not match a missing member",
			match:  map[string]string{"baggage[session]": ""},
			header: http.Header{"Baggage": {"tenant=acme"}},
			want:   false,
		},
		{
			name:   "empty value does not match a missing header",
			match:  map[string]string{"baggage[session]": ""},
			header: http.Header{},
			want:   false,
		},
		{
			name:   "regexp member value",
			match:  map[string]string{"baggage[session]": "^alice-[0-9]+$"},
			header: http.Header{"Baggage": {"tenant=acme,session=alice-42"}},
			want:   true,
		},
		{
			name:   "two members",
			match:  map[string]string{"baggage[session]": "alice-1", "baggage[tenant]": "acme"},
			header: http.Header{"Baggage": {"tenant=acme,session=alice-1"}},
			want:   true,
		},
		{
			name:   "cookie",
			match:  map[string]string{"cookie[sid]": "alice-1"},
			header: http.Header{"Cookie": {"theme=dark; sid=alice-1"}},
			want:   true,
		},
		{
			name:   "tracestate",
			match:  map[string]string{"tracestate[vendor]": "alice-1"},
			header: http.Header{"Tracestate": {"other=x,vendor=alice-1"}},
			want:   true,
		},
		{
			name:   "quoted value with separators",
			match:  map[string]string{"forwarded[for]": "_hidden,1;x"},
			header: http.Header{"Forwarded": {`for="_hidden,1;x";proto=https, for=192.0.2.60`}},
			want:   true,
		},
		{
			name:   "quoted-pair in quoted value",
			match:  map[string]string{"x-list[id]": `a"b`},
			header: http.Header{"X-List": {`id="a\"b"`}},
			want:   true,
		},
		{
			name:   "only the first pair of an element is a member",
			match:  map[string]string{"forwarded[proto]": "https"},
			header: http.Header{"Forwarded": {"for=192.0.2.60;proto=https"}},
			want:   false,
		},
		{
			name:   "property alone is not a member",
			match:  map[string]string{"baggage[session]": "bob-2"},
			header: http.Header{"Baggage": {"tenant=acme;session=bob-2"}},
			want:   false,
		},
		{
			name:   "value with '=' in it",
			match:  map[string]string{"baggage[token]": "YQ=="},
			header: http.Header{"Baggage": {"token=YQ=="}},
			want:   true,
		},
		{
			name:   "cookie in second header line",
			match:  map[string]string{"cookie[sid]": "alice-1"},
			header: http.Header{"Cookie": {"theme=dark", "sid=alice-1"}},
			want:   true,
		},
		{
			name:   "quoted cookie value",
			match:  map[string]string{"cookie[sid]": "alice-1"},
			header: http.Header{"Cookie": {`theme=dark; sid="alice-1"`}},
			want:   true,
		},
		{
			name:   "cookie values are not split on commas",
			match:  map[string]string{"cookie[sid]": "alice-1"},
			header: http.Header{"Cookie": {"theme=dark,sid=alice-1"}},
			want:   false,
		},
		{
			name:   "missing cookie",
			match:  map[string]string{"cookie[sid]": "alice-1"},
			header: http.Header{"Cookie": {"theme=dark"}},
			want:   false,
		},
		{
			name:   "forwarded first occurrence",
			match:  map[string]string{"forwarded[for]": "192.0.2.60"},
			header: http.Header{"Forwarded": {`for=192.0.2.60;proto=https, for="[2001:db8::1]"`}},
			want:   true,
		},
		{
			name:   "whole header still matched as a header",
			match:  map[string]string{"baggage": "tenant=acme"},
			header: http.Header{"Baggage": {"tenant=acme"}},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hm := NewHeaders(tt.match)
			assert.Equal(t, tt.want, hm.Matches(tt.header))
		})
	}
}

func Test_NewHeaders_memberKey(t *testing.T) {
	hm := NewHeaders(map[string]string{"x-list[Session]": "alice-1"}).HeaderMap()
	assert.Contains(t, hm, "X-List[Session]")
}
