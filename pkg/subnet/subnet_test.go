package subnet

import (
	"bufio"
	"net"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func Test_covers(t *testing.T) {
	_, network1, _ := net.ParseCIDR("10.127.0.0/16")
	_, network2, _ := net.ParseCIDR("10.127.201.0/24")
	assert.True(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.202.0/24")
	assert.True(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/16")
	assert.True(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/18")
	assert.True(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.124.0.0/14")
	assert.False(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.201.0/8")
	assert.False(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.128.0.0/16")
	assert.False(t, Covers(network1, network2))

	_, network1, _ = net.ParseCIDR("10.127.192.0/18")
	_, network2, _ = net.ParseCIDR("10.127.192.0/18")
	assert.True(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/16")
	assert.False(t, Covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.192.0/19")
	assert.True(t, Covers(network1, network2))

	_, network1, _ = net.ParseCIDR("192.168.0.0/21")
	_, network2, _ = net.ParseCIDR("192.168.8.0/24")
	assert.False(t, Covers(network1, network2))
}

func TestCoveringCIDRs(t *testing.T) {
	ips := loadIPs(t)
	ipNets := CoveringCIDRs(ips)
	require.Equal(t, 4, len(ipNets))
	require.Equal(t, &net.IPNet{
		IP:   net.IP{10, 101, 128, 0},
		Mask: net.CIDRMask(18, 32),
	}, ipNets[0])
	require.Equal(t, &net.IPNet{
		IP:   net.IP{172, 20, 0, 0},
		Mask: net.CIDRMask(16, 32),
	}, ipNets[1])
	require.Equal(t, &net.IPNet{
		IP:   net.IP{0x20, 0x01, 0x0d, 0xb8, 0x33, 0x33, 0x44, 0x44, 0x55, 0x55, 0x66, 0x66, 0x77, 0x00, 0x00, 0x00},
		Mask: net.CIDRMask(104, 128),
	}, ipNets[2])
	require.Equal(t, &net.IPNet{
		IP:   net.IP{0x20, 0x01, 0x0d, 0xb8, 0x33, 0x33, 0xab, 0x32, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0},
		Mask: net.CIDRMask(79, 128),
	}, ipNets[3])
}

func loadIPs(t *testing.T) []net.IP {
	ipf, err := os.Open("testdata/ips.txt")
	require.NoError(t, err)
	defer ipf.Close()

	ips := make([]net.IP, 0, 1500)
	rd := bufio.NewScanner(ipf)
	for rd.Scan() {
		ip := net.ParseIP(rd.Text())
		if ip == nil {
			t.Fatal("bad ip")
		}
		ips = append(ips, ip)
	}
	return ips
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name    string
		subnets []*net.IPNet
		want    []*net.IPNet
	}{
		{
			name: "Removes equal subnets",
			subnets: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
			},
			want: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
		{
			name: "Removes covered subnets",
			subnets: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(24, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
			},
			want: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
		{
			name: "Removes covered subnets reverse",
			subnets: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(24, 32),
				},
			},
			want: []*net.IPNet{
				{
					IP:   iputil.Parse("192.168.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
				{
					IP:   iputil.Parse("192.172.0.0"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := Unique(tt.subnets); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unique() = %v, want %v", got, tt.want)
			}
		})
	}
}
