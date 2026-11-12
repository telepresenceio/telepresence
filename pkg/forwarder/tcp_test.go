package forwarder

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type pipeDialer struct {
	conn net.Conn
}

func (d pipeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return d.conn, nil
}

func TestForwardTCP_CancelClosesConnections(t *testing.T) {
	client, incoming := net.Pipe()
	defer client.Close()
	defer incoming.Close()
	outgoing, target := net.Pipe()
	defer outgoing.Close()
	defer target.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := NewTCP(0, tunnel.AgentToClient, netip.MustParseAddrPort("127.0.0.1:8080"), WithDialer(pipeDialer{outgoing}))
	done := make(chan error, 1)
	go pprof.Do(ctx, pprof.Labels("forwarder-test", t.Name()), func(ctx context.Context) {
		done <- f.Forward(ctx, incoming)
	})

	// Exchange data in both directions before cancelling the idle connection.
	for _, pair := range [][2]net.Conn{{client, target}, {target, client}} {
		written := make(chan error, 1)
		go func() {
			_, err := pair[0].Write([]byte("hello"))
			written <- err
		}()
		require.NoError(t, pair[1].SetReadDeadline(time.Now().Add(time.Second)))
		buf := make([]byte, 5)
		_, err := io.ReadFull(pair[1], buf)
		require.NoError(t, err)
		require.Equal(t, "hello", string(buf))
		require.NoError(t, <-written)
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("forwarding did not stop after cancellation")
	}
	for _, conn := range []net.Conn{client, target} {
		_, err := conn.Read(make([]byte, 1))
		require.ErrorIs(t, err, io.EOF)
	}

	// The copy goroutines inherit the label and must exit after both sockets close.
	require.Eventually(t, func() bool {
		var goroutines bytes.Buffer
		if err := pprof.Lookup("goroutine").WriteTo(&goroutines, 1); err != nil {
			return false
		}
		return !strings.Contains(goroutines.String(), `"forwarder-test":"`+t.Name()+`"`)
	}, time.Second, time.Millisecond, "cancelled forwarding leaked a goroutine")
}
