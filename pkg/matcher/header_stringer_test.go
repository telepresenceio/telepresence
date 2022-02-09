package matcher

import (
	"net/http"
	"strings"
	"testing"
)

func TestStringer_String(t *testing.T) {
	newStringer := func(s ...string) HeaderStringer {
		h := http.Header{}
		for i := 0; i < len(s); i += 2 {
			k := s[i]
			vs := strings.Split(s[i+1], ";") // THe semicolon is deliberate to ensure that comma is the result of formatting
			h.Set(k, vs[0])
			for i := 1; i < len(vs); i++ {
				h.Add(k, vs[i])
			}
		}
		return HeaderStringer(h)
	}

	tests := []struct {
		name string
		s    HeaderStringer
		want string
	}{
		{
			"one header, single value",
			newStringer("hdr-one", "val"),
			"Hdr-One: val",
		},
		{
			"one header, multiple values",
			newStringer("hdr-one", "val1;val2;val3"),
			"Hdr-One: val1,val2,val3",
		},
		{
			"multiple headers, single value",
			newStringer("hdr-one", "val1;val2", "hdr-two", "the value"),
			"Hdr-One: val1,val2\nHdr-Two: the value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
