package dnsproxy

import (
	"context"
	"net"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func TestLookup(t *testing.T) {
	type tType struct {
		qType uint16
		qName string
	}
	tests := []tType{
		{
			dns.TypeA,
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
	// AAAA returns an error on Windows:
	// "getaddrinfow: The requested name is valid, but no data of the requested type was found"
	if runtime.GOOS != "windows" {
		tests = append(tests, tType{
			dns.TypeAAAA,
			"google.com.",
		})
	}
	for _, tt := range tests {
		t.Run(dns.TypeToString[tt.qType], func(t *testing.T) {
			if tt.qType == dns.TypeSRV && runtime.GOOS == "darwin" {
				t.Skip("SRV sporadically fails to parse reply on darwin")
			}
			ctx := dlog.NewTestContext(t, false)
			got, _, err := Lookup(ctx, tt.qType, tt.qName)
			require.NoError(t, err)
			require.Greater(t, len(got), 0)
		})
	}
}

func TestPtrAddress_v4(t *testing.T) {
	ip, err := PtrAddress("32.127.168.192.in-addr.arpa.")
	require.NoError(t, err)
	require.Equal(t, net.IP{192, 168, 127, 32}, ip)
}

func TestPtrAddress_v6(t *testing.T) {
	ip, err := PtrAddress("b.a.9.8.7.6.5.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.")
	require.NoError(t, err)
	require.Equal(t, iputil.Parse("2001:db8::567:89ab"), ip)
}

func TestTimedExternalLookup(t *testing.T) {
	t.Skip("needs some more love, the diff between lookups is not predicable")
	names := []string{
		"google.com",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			wips, err := net.LookupIP(name)
			require.NoError(t, err)
			want := iputil.UniqueSorted(wips)
			got := TimedExternalLookup(context.Background(), name, 2*time.Second).UniqueSorted()
			if !reflect.DeepEqual(got, want) {
				t.Errorf("TimedExternalLookup() = %v, want %v", got, want)
			}
		})
	}
}
