package manager

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func Test_dnsLookup(t *testing.T) {
	tests := []struct {
		qType uint16
		qName string
	}{
		{
			dns.TypeA,
			"google.com.",
		},
		{
			dns.TypeAAAA,
			"google.com.",
		},
		{
			dns.TypeCNAME,
			"_smpp_._tcp.golang.org.",
		},
		{
			dns.TypePTR,
			"78.217.250.142.in-addr.arpa.",
		},
		{
			dns.TypeMX,
			"gmail.com.",
		},
		{
			dns.TypeTXT,
			"dns.google.",
		},
		{
			dns.TypeSRV,
			"_myservice._tcp.tada.se.",
		},
	}
	for _, tt := range tests {
		t.Run(dns.TypeToString[tt.qType], func(t *testing.T) {
			ctx := dlog.NewTestContext(t, false)
			got, err := dnsLookup(ctx, tt.qType, tt.qName)
			require.NoError(t, err)
			require.Greater(t, len(got), 0)
		})
	}
}

func Test_ptrToAddress_v4(t *testing.T) {
	ip, err := ptrAddress("32.127.168.192.in-addr.arpa.")
	require.NoError(t, err)
	require.Equal(t, net.IP{192, 168, 127, 32}, ip)
}

func Test_ptrToAddress_v6(t *testing.T) {
	ip, err := ptrAddress("b.a.9.8.7.6.5.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.")
	require.NoError(t, err)
	require.Equal(t, iputil.Parse("2001:db8::567:89ab"), ip)
}
