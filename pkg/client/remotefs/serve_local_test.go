package remotefs

import (
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestServeLocal_hostBindsLoopbackOnly(t *testing.T) {
	var other netip.Addr
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			other, _ = netip.AddrFromSlice(ipn.IP.To4())
			break
		}
	}
	if !other.IsValid() {
		t.Skip("no non-loopback IPv4 address")
	}

	ports, err := ioutil.FreePortsTCP(1)
	require.NoError(t, err)
	port := ports[0].Port()
	ctx := t.Context()
	go func() {
		_ = serveLocal(ctx, types.PortAndProto{Port: port, Proto: types.ProtoTCP}, netip.MustParseAddrPort("127.0.0.1:1"), false)
	}()

	lo := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	require.Eventually(t, func() bool {
		c, err := net.Dial("tcp", lo)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond)

	// A wildcard listener would make this bind fail.
	l, err := net.Listen("tcp", net.JoinHostPort(other.String(), strconv.Itoa(int(port))))
	require.NoError(t, err)
	l.Close()
}
