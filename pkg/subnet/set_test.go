package subnet

import (
	"net/netip"
	"reflect"
	"testing"
)

var (
	oneCIDR   = netip.MustParsePrefix("192.168.0.0/24")
	twoCIDR   = netip.MustParsePrefix("192.168.1.0/24")
	threeCIDR = netip.MustParsePrefix("192.168.2.0/24")
)

func TestSet_Add(t *testing.T) {
	tests := []struct {
		name  string
		cidrs []netip.Prefix
		want  Set
	}{
		{
			name:  "works with nil",
			cidrs: nil,
			want:  Set{},
		},
		{
			name:  "adds uniquely",
			cidrs: []netip.Prefix{oneCIDR, twoCIDR, oneCIDR, twoCIDR},
			want:  NewSet([]netip.Prefix{oneCIDR, twoCIDR}),
		},
		{
			name:  "works with nil",
			cidrs: nil,
			want:  NewSet(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewSet(tt.cidrs); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("makeCIDRMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSet_String(t *testing.T) {
	tests := []struct {
		name string
		s    Set
		want string
	}{
		{
			name: "nil is ok",
			s:    nil,
			want: "nil",
		},
		{
			name: "zero elements is just brackets",
			s:    Set{},
			want: "[]",
		},
		{
			name: "one element is without space",
			s:    NewSet([]netip.Prefix{oneCIDR}),
			want: "[192.168.0.0/24]",
		},
		{
			name: "output is space separated and sorted",
			s:    NewSet([]netip.Prefix{threeCIDR, oneCIDR, twoCIDR}),
			want: "[192.168.0.0/24 192.168.1.0/24 192.168.2.0/24]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSet_AppendSortedTo(t *testing.T) {
	tests := []struct {
		name    string
		s       Set
		subnets []netip.Prefix
		want    []netip.Prefix
	}{
		{
			name:    "appends sorted",
			s:       NewSet([]netip.Prefix{threeCIDR, oneCIDR, twoCIDR}),
			subnets: []netip.Prefix{threeCIDR},
			want:    []netip.Prefix{threeCIDR, oneCIDR, twoCIDR, threeCIDR},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.AppendSortedTo(tt.subnets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AppendSortedTo() = %v, want %v", got, tt.want)
			}
		})
	}
}
