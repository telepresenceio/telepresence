package vip

import (
	"maps"
	"net/netip"
	"testing"

	"github.com/telepresenceio/dlib/v2/dlog"
)

type localIPProviderTest struct {
	cidrs     []netip.Prefix
	generator Generator
	mapped    map[netip.Addr]netip.Addr
}

func (l *localIPProviderTest) MapsIPv4() bool {
	for _, p := range l.cidrs {
		if p.Addr().Is4() {
			return true
		}
	}
	return false
}

func (l *localIPProviderTest) MapsIPv6() bool {
	for _, p := range l.cidrs {
		if p.Addr().Is6() {
			return true
		}
	}
	return false
}

func (l *localIPProviderTest) GetLocalIP(remoteIP netip.Addr) (netip.Addr, error) {
	if lip, ok := l.mapped[remoteIP]; ok {
		return lip, nil
	}
	for _, p := range l.cidrs {
		if p.Contains(remoteIP) {
			lip, err := l.generator.Next()
			if err != nil {
				return remoteIP, err
			}
			l.mapped[remoteIP] = lip
			return lip, nil
		}
	}
	return remoteIP, nil
}

func Test_translateEnvironmentIPs(t *testing.T) {
	tests := []struct {
		name  string
		cidr  string
		vCidr string
		ip    string
		want  string
	}{
		{
			"IPV4 URI",
			"10.110.210.0/24",
			"100.156.200.0/24",
			"tcp://10.110.210.159:80",
			"tcp://100.156.200.1:80",
		},
		{
			"IPV4",
			"10.110.210.0/24",
			"100.156.200.0/24",
			"10.110.210.159",
			"100.156.200.1",
		},
		{
			"IPV4 list",
			"10.110.210.0/24",
			"100.156.200.0/24",
			`["10.110.210.8", "10.110.210.9", "10.110.210.9", "192.168.1.3"]`,
			`["100.156.200.1", "100.156.200.2", "100.156.200.2", "192.168.1.3"]`,
		},
		{
			"IPV4 in IPV6",
			"::ffff:10.110.210.0/96",
			"::ffff:100.156.200.0/120",
			"::ffff:10.110.210.8",
			"::ffff:100.156.200.1",
		},
		{
			"IPV6 URI",
			"::ffff:10.110.210.0/96",
			"::ffff:100.156.200.0/120",
			"tcp://[::ffff:10.110.210.8]:53",
			"tcp://[::ffff:100.156.200.1]:53",
		},
		{
			"IPV4 leading ndot",
			"100.156.200.0/24",
			"10.110.210.0/24",
			"2.10.110.210.8",
			"2.10.110.210.8",
		},
	}

	ctx := dlog.NewTestContext(t, false)
	for _, tt := range tests {
		provider := &localIPProviderTest{
			generator: NewGenerator(netip.MustParsePrefix(tt.vCidr)),
			mapped:    make(map[netip.Addr]netip.Addr),
			cidrs:     []netip.Prefix{netip.MustParsePrefix(tt.cidr)},
		}
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"key": tt.ip}
			want := map[string]string{"key": tt.want}
			TranslateEnvironmentIPs(ctx, env, provider)
			if !maps.Equal(env, want) {
				t.Errorf("TranslateEnvironmentIPs() = %v, want %v", env, want)
			}
		})
	}
}
