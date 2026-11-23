package rootd

import (
	"testing"

	dns2 "github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
)

// A cluster test cannot make a traffic-manager send an A record with a bad length.
func TestCheckAddressRecords(t *testing.T) {
	require.NoError(t, checkAddressRecords(dnsproxy.RRs{
		&dns2.A{A: []byte{10, 0, 0, 1}},
		&dns2.AAAA{AAAA: make([]byte, 16)},
		&dns2.A{},
		&dns2.AAAA{},
	}))
	require.Error(t, checkAddressRecords(dnsproxy.RRs{&dns2.A{A: []byte{1, 2}}}))
	require.Error(t, checkAddressRecords(dnsproxy.RRs{&dns2.AAAA{AAAA: []byte{1}}}))
}
