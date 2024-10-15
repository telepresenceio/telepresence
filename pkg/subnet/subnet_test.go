package subnet

import (
	"bufio"
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_covers(t *testing.T) {
	network1, _ := netip.ParsePrefix("10.127.0.0/16")
	network2, _ := netip.ParsePrefix("10.127.201.0/24")
	assert.True(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.202.0/24")
	assert.True(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.0.0/16")
	assert.True(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.0.0/18")
	assert.True(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.124.0.0/14")
	assert.False(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.201.0/8")
	assert.False(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.128.0.0/16")
	assert.False(t, Covers(network1, network2))

	network1, _ = netip.ParsePrefix("10.127.192.0/18")
	network2, _ = netip.ParsePrefix("10.127.192.0/18")
	assert.True(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.0.0/16")
	assert.False(t, Covers(network1, network2))

	network2, _ = netip.ParsePrefix("10.127.192.0/19")
	assert.True(t, Covers(network1, network2))

	network1, _ = netip.ParsePrefix("192.168.0.0/21")
	network2, _ = netip.ParsePrefix("192.168.8.0/24")
	assert.False(t, Covers(network1, network2))
}

func TestCoveringPrefixes(t *testing.T) {
	ips := loadAddrs(t)
	ipNets := CoveringPrefixes(ips)
	require.Equal(t, 4, len(ipNets))
	require.Equal(t, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 101, 128, 0}), 18), ipNets[0])
	require.Equal(t, netip.PrefixFrom(netip.AddrFrom4([4]byte{172, 20, 0, 0}), 16), ipNets[1])
	require.Equal(t, netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0x33, 0x33, 0x44, 0x44, 0x55, 0x55, 0x66, 0x66, 0x77, 0x00, 0x00, 0x00}), 104), ipNets[2])
	require.Equal(t, netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0x33, 0x33, 0xab, 0x32, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0}), 79), ipNets[3])
}

func loadAddrs(t *testing.T) []netip.Addr {
	ipf, err := os.Open("testdata/ips.txt")
	require.NoError(t, err)
	defer ipf.Close()

	ips := make([]netip.Addr, 0, 1500)
	rd := bufio.NewScanner(ipf)
	for rd.Scan() {
		ip, err := netip.ParseAddr(rd.Text())
		if err != nil {
			t.Fatal("bad ip")
		}
		ips = append(ips, ip)
	}
	return ips
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name    string
		subnets []netip.Prefix
		want    []netip.Prefix
	}{
		{
			name: "Removes equal subnets",
			subnets: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
			},
			want: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
			},
		},
		{
			name: "Removes covered subnets",
			subnets: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/24"),
				netip.MustParsePrefix("192.172.0.0/16"),
				netip.MustParsePrefix("192.168.0.0/16"),
			},
			want: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
			},
		},
		{
			name: "Removes covered subnets reverse",
			subnets: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
				netip.MustParsePrefix("192.168.0.0/24"),
			},
			want: []netip.Prefix{
				netip.MustParsePrefix("192.168.0.0/16"),
				netip.MustParsePrefix("192.172.0.0/16"),
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
