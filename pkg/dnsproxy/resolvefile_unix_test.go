//go:build !windows

package dnsproxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadResolveFile(t *testing.T) {
	const fileContent = `
# Some comment starting with hash
; Some comment starting with semicolon
port 33764
domain example.com
nameserver 1.1.1.1
nameserver  8.8.8.8
search example.com svc.example.com ns.svc.example.com
sortlist 130.155.160.0/255.255.240.0 130.155.0.0
options timeout:10 single-request-reopen
; Another comment
# And yet another comment
`
	rdr := strings.NewReader(fileContent)
	rf, err := ParseResolveFile("test.conf", rdr)
	require.NoError(t, err)
	require.Equal(t, rf.Port, 33764)
	require.Equal(t, rf.Domain, "example.com")
	require.Equal(t, rf.Nameservers, []string{"1.1.1.1", "8.8.8.8"})
	require.Equal(t, rf.Search, []string{"example.com", "svc.example.com", "ns.svc.example.com"})
	require.Equal(t, rf.SortList, []string{"130.155.160.0/255.255.240.0", "130.155.0.0"})
	require.Equal(t, rf.Options, []string{"timeout:10", "single-request-reopen"})
}
