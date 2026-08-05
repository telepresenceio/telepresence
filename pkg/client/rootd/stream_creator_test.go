package rootd

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestCreateSessionCleansDNSRoutingBeforeKubeconfig(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())

	called := false
	cleanupDNSRouting := func(context.Context) {
		called = true
	}

	_, err := createSession(ctx, ctx, &rpc.NetworkConfig{
		KubeconfigData: []byte("not: [valid"),
	}, make(chan time.Time), cleanupDNSRouting)

	require.Error(t, err)
	require.True(t, called)
}

func TestIsAlsoProxyDestination(t *testing.T) {
	s := &session{
		alsoProxySubnets: []netip.Prefix{
			netip.MustParsePrefix("240.240.0.0/16"),
			netip.MustParsePrefix("fd00::/8"),
		},
	}

	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("240.240.0.33")))
	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("fd00::1")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("10.128.0.10")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("2001:db8::1")))
}

func newTestStreamSession(ctx context.Context) *session {
	return &session{
		Cluster: &k8s.Cluster{
			Kubeconfig: &k8s.Kubeconfig{
				Context: ctx,
			},
		},
		session: &manager.SessionInfo{SessionId: "test-session"},
	}
}

func TestStreamCreatorRedirectsLocalClient(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	remote := types.AddrPortProto{
		AddrPort: netip.MustParseAddrPort("10.0.0.10:8000"),
		Proto:    types.ProtoTCP,
	}
	s := newTestStreamSession(ctx)
	s.addLocalClientRedirect(remote, listener.Addr().(*net.TCPAddr).AddrPort().Port())
	target, ok := s.localClientRedirectTarget(tunnel.NewConnID(
		types.ProtoTCP,
		netip.MustParseAddrPort("192.168.0.2:43210"),
		remote.AddrPort,
	))
	require.True(t, ok)
	require.Equal(t, listener.Addr().(*net.TCPAddr).AddrPort(), target)
	redirects := s.listLocalClientRedirects()
	require.Len(t, redirects, 1)
	var listedRemote types.AddrPortProto
	require.NoError(t, listedRemote.UnmarshalBinary(redirects[0].DstHostPort))
	require.Equal(t, remote, listedRemote)
	require.Equal(t, uint32(listener.Addr().(*net.TCPAddr).AddrPort().Port()), redirects[0].LocalPort)

	stream, err := s.streamCreator()(ctx, tunnel.NewConnID(
		types.ProtoTCP,
		netip.MustParseAddrPort("192.168.0.2:43210"),
		remote.AddrPort,
	))
	require.NoError(t, err)

	clientConn, vifConn := net.Pipe()
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	tunnel.NewConnEndpoint(stream, vifConn, cancel, nil, nil).Start(ctx)

	_, err = clientConn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(clientConn, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))

	s.removeLocalClientRedirect(remote)
	_, ok = s.localClientRedirectTarget(tunnel.NewConnID(
		types.ProtoTCP,
		netip.MustParseAddrPort("192.168.0.2:43210"),
		remote.AddrPort,
	))
	require.False(t, ok)
	require.Empty(t, s.listLocalClientRedirects())
}

func TestStreamCreatorAppliesRemoteRerouteBeforeLocalClientRedirect(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	original := types.AddrPortProto{
		AddrPort: netip.MustParseAddrPort("10.0.0.10:8000"),
		Proto:    types.ProtoTCP,
	}
	s := newTestStreamSession(ctx)
	s.rerouteRemotePort(original, 8080)
	s.addLocalClientRedirect(original, listener.Addr().(*net.TCPAddr).AddrPort().Port())

	stream, err := s.streamCreator()(ctx, tunnel.NewConnID(
		types.ProtoTCP,
		netip.MustParseAddrPort("192.168.0.2:43210"),
		netip.MustParseAddrPort("10.0.0.10:8080"),
	))
	require.NoError(t, err)

	clientConn, vifConn := net.Pipe()
	defer clientConn.Close()
	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	tunnel.NewConnEndpoint(stream, vifConn, cancel, nil, nil).Start(ctx)

	_, err = clientConn.Write([]byte("pong"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(clientConn, buf)
	require.NoError(t, err)
	require.Equal(t, "pong", string(buf))
}
