//go:build linux

package routing

import (
	"syscall" //nolint:depguard // sys/unix does not have routing constants on all supported platforms
	"testing"

	"github.com/vishvananda/netlink"
)

func TestShouldSkipNetlinkRoute(t *testing.T) {
	tests := []struct {
		name string
		rt   netlink.Route
		want bool
	}{
		{
			name: "unicast interface route",
			rt: netlink.Route{
				Type:      syscall.RTN_UNICAST,
				LinkIndex: 1,
			},
		},
		{
			name: "zero link index",
			rt: netlink.Route{
				Type: syscall.RTN_UNICAST,
			},
			want: true,
		},
		{
			name: "blackhole route",
			rt: netlink.Route{
				Type:      syscall.RTN_BLACKHOLE,
				LinkIndex: 1,
			},
			want: true,
		},
		{
			name: "unreachable route",
			rt: netlink.Route{
				Type:      syscall.RTN_UNREACHABLE,
				LinkIndex: 1,
			},
			want: true,
		},
		{
			name: "prohibit route",
			rt: netlink.Route{
				Type:      syscall.RTN_PROHIBIT,
				LinkIndex: 1,
			},
			want: true,
		},
		{
			name: "throw route",
			rt: netlink.Route{
				Type:      syscall.RTN_THROW,
				LinkIndex: 1,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipNetlinkRoute(&tt.rt); got != tt.want {
				t.Fatalf("shouldSkipNetlinkRoute() = %v, want %v", got, tt.want)
			}
		})
	}
}
