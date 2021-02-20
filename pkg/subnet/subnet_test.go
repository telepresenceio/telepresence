package subnet

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_covers(t *testing.T) {
	_, network1, _ := net.ParseCIDR("10.127.0.0/16")
	_, network2, _ := net.ParseCIDR("10.127.201.0/24")
	assert.True(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.202.0/24")
	assert.True(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/16")
	assert.True(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/18")
	assert.True(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.124.0.0/14")
	assert.False(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.201.0/8")
	assert.False(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.128.0.0/16")
	assert.False(t, covers(network1, network2))

	_, network1, _ = net.ParseCIDR("10.127.192.0/18")
	_, network2, _ = net.ParseCIDR("10.127.192.0/18")
	assert.True(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.0.0/16")
	assert.False(t, covers(network1, network2))

	_, network2, _ = net.ParseCIDR("10.127.192.0/19")
	assert.True(t, covers(network1, network2))

	_, network1, _ = net.ParseCIDR("192.168.0.0/21")
	_, network2, _ = net.ParseCIDR("192.168.8.0/24")
	assert.False(t, covers(network1, network2))
}

func Test_findAvailableIPV4CIDR(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return nil, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "10.0.0.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(24, 32)}}, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "10.0.1.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_all_C_in_10_10_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(16, 32)}}, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "10.1.0.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_all_B_in_10_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(8, 32)}}, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "17.16.0.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_all_10_and_17_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.IP{17, 16, 0, 0}, Mask: net.CIDRMask(12, 32)},
		}, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "192.168.0.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_all_10_17_and_some_192_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.IP{10, 0, 0, 0}, Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.IP{17, 16, 0, 0}, Mask: net.CIDRMask(12, 32)},
			&net.IPNet{IP: net.IP{192, 168, 0, 0}, Mask: net.CIDRMask(21, 32)},
		}, nil
	}
	got, err := FindAvailableClassC()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "192.168.8.0/24", got.String())
}

func Test_findAvailableIPV4CIDR_all_busy(t *testing.T) {
	interfaceAddrs = func() ([]net.Addr, error) {
		addrs := make([]net.Addr, 512+16)
		for i := 0; i < 256; i++ {
			addrs[i] = &net.IPNet{
				IP:   net.IP{10, byte(i), 0, 1},
				Mask: net.IPMask{255, 255, 0, 0},
			}
		}
		for i := 0; i < 16; i++ {
			addrs[256+i] = &net.IPNet{
				IP:   net.IP{17, byte(i + 16), 0, 1},
				Mask: net.IPMask{255, 255, 0, 0},
			}
		}
		for i := 0; i < 256; i++ {
			addrs[256+16+i] = &net.IPNet{
				IP:   net.IP{192, 168, byte(i), 1},
				Mask: net.IPMask{255, 255, 255, 0},
			}
		}
		return addrs, nil
	}
	_, err := FindAvailableClassC()
	assert.Error(t, err)
}
