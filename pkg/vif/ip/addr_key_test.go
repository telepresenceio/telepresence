package ip

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func TestAddress_String(t *testing.T) {
	tests := []struct {
		name string
		a    AddrKey
		want string
	}{
		{
			"zero address",
			"",
			"invalid address",
		},
		{
			"IPv4 address",
			MakeAddrKey(iputil.Parse("192.168.0.1"), 8080),
			"192.168.0.1:8080",
		},
		{
			"IPv6 address",
			MakeAddrKey(iputil.Parse("2001:0db8:85a3:0000:0000:8a2e:0370:7334"), 8080),
			"[2001:db8:85a3::8a2e:370:7334]:8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}
