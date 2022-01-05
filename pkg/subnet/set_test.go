package subnet

import (
	"net"
	"reflect"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

var (
	oneCIDR = &net.IPNet{
		IP:   iputil.Parse("192.168.0.0"),
		Mask: net.CIDRMask(24, 32),
	}
	twoCIDR = &net.IPNet{
		IP:   iputil.Parse("192.168.1.0"),
		Mask: net.CIDRMask(24, 32),
	}
	threeCIDR = &net.IPNet{
		IP:   iputil.Parse("192.168.2.0"),
		Mask: net.CIDRMask(24, 32),
	}
)

func TestSet_Add(t *testing.T) {
	tests := []struct {
		name  string
		cidrs []*net.IPNet
		want  Set
	}{
		{
			name:  "works with nil",
			cidrs: nil,
			want:  Set{},
		},
		{
			name:  "adds uniquely",
			cidrs: []*net.IPNet{oneCIDR, twoCIDR, oneCIDR, twoCIDR},
			want:  NewSet([]*net.IPNet{oneCIDR, twoCIDR}),
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
			s:    NewSet([]*net.IPNet{oneCIDR}),
			want: "[192.168.0.0/24]",
		},
		{
			name: "output is space separated and sorted",
			s:    NewSet([]*net.IPNet{threeCIDR, oneCIDR, twoCIDR}),
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
		subnets []*net.IPNet
		want    []*net.IPNet
	}{
		{
			name:    "appends sorted",
			s:       NewSet([]*net.IPNet{threeCIDR, oneCIDR, twoCIDR}),
			subnets: []*net.IPNet{threeCIDR},
			want:    []*net.IPNet{threeCIDR, oneCIDR, twoCIDR, threeCIDR},
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
