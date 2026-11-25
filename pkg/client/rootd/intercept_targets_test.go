package rootd

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestDialAllowed(t *testing.T) {
	ap := netip.MustParseAddrPort
	bin := func(s string) []byte {
		b, err := ap(s).MarshalBinary()
		require.NoError(t, err)
		return b
	}
	src := ap("10.1.2.3:4000")
	s := &session{}
	id := func(p types.Proto, dst string) tunnel.ConnID { return tunnel.NewConnID(p, src, ap(dst)) }

	// Nothing is allowed before the targets are declared.
	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "127.0.0.1:8080")))

	s.SetInterceptTargets(context.Background(), []*rpc.InterceptTarget{
		{Namespace: "ns", Workload: "echo", Protocol: "TCP", Target: bin("127.0.0.1:8080")},
		{Namespace: "ns", Workload: "echo", Protocol: "", Target: bin("[fd00::1]:9090")},
		{Namespace: "ns", Workload: "echo", Protocol: "UDP", Target: bin("[::ffff:127.0.0.1]:5353")},
		{Namespace: "ns", Workload: "bogus", Protocol: "TCP", Target: []byte{1, 2, 3}},
	})
	assert.True(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "127.0.0.1:8080")))
	assert.True(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "[fd00::1]:9090")))
	assert.True(t, s.dialAllowed("ns", "echo", id(types.ProtoUDP, "127.0.0.1:5353")))

	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "127.0.0.1:8081")))
	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "127.0.0.2:8080")))
	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoUDP, "127.0.0.1:8080")))
	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "169.254.169.254:80")))
	assert.False(t, s.dialAllowed("other", "echo", id(types.ProtoTCP, "127.0.0.1:8080")))
	assert.False(t, s.dialAllowed("ns", "other", id(types.ProtoTCP, "127.0.0.1:8080")))
	assert.False(t, s.dialAllowed("ns", "echo", tunnel.ConnID("short")))
	assert.False(t, s.dialAllowed("ns", "bogus", id(types.ProtoTCP, "127.0.0.1:8080")))

	// A new declaration replaces the old one.
	s.SetInterceptTargets(context.Background(), nil)
	assert.False(t, s.dialAllowed("ns", "echo", id(types.ProtoTCP, "127.0.0.1:8080")))
}
