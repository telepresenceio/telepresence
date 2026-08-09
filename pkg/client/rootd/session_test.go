package rootd

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func TestManagerIPInPodSubnets(t *testing.T) {
	podSubnets := []*manager.IPNet{
		iputil.PrefixToRPC(netip.MustParsePrefix("10.42.0.0/23")),
		iputil.PrefixToRPC(netip.MustParsePrefix("fd00:10:42::/64")),
	}
	tests := []struct {
		name string
		info *manager.ClusterInfo
		want bool
	}{
		{
			name: "pod IP in pod subnet",
			info: &manager.ClusterInfo{
				ManagerPodIp: netip.MustParseAddr("10.42.1.3").AsSlice(),
				PodSubnets:   podSubnets,
			},
			want: true,
		},
		{
			name: "4-in-6 mapped pod IP in pod subnet",
			info: &manager.ClusterInfo{
				ManagerPodIp: netip.MustParseAddr("::ffff:10.42.1.3").AsSlice(),
				PodSubnets:   podSubnets,
			},
			want: true,
		},
		{
			name: "IPv6 pod IP in pod subnet",
			info: &manager.ClusterInfo{
				ManagerPodIp: netip.MustParseAddr("fd00:10:42::3").AsSlice(),
				PodSubnets:   podSubnets,
			},
			want: true,
		},
		{
			name: "node IP of a host-network manager",
			info: &manager.ClusterInfo{
				ManagerPodIp: netip.MustParseAddr("192.168.56.2").AsSlice(),
				PodSubnets:   podSubnets,
			},
			want: false,
		},
		{
			name: "no pod subnets",
			info: &manager.ClusterInfo{
				ManagerPodIp: netip.MustParseAddr("10.42.1.3").AsSlice(),
			},
			want: false,
		},
		{
			name: "no manager pod IP",
			info: &manager.ClusterInfo{
				PodSubnets: podSubnets,
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, managerIPInPodSubnets(tt.info))
		})
	}
}
