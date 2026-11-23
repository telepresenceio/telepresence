package rootd

import (
	"testing"

	dns2 "github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
)

// A cluster test cannot make a traffic-manager send an A record without data.
func TestCheckAddressRecords(t *testing.T) {
	require.NoError(t, checkAddressRecords(dnsproxy.RRs{
		&dns2.A{A: []byte{10, 0, 0, 1}},
		&dns2.AAAA{AAAA: make([]byte, 16)},
	}))
	require.Error(t, checkAddressRecords(dnsproxy.RRs{&dns2.A{}}))
	require.Error(t, checkAddressRecords(dnsproxy.RRs{&dns2.AAAA{AAAA: []byte{1}}}))
}
