package tunnel

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/nettest"
	"golang.org/x/sync/errgroup"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// TestStreamConn uses nettest.TestConn to test the StreamConn implementation.
func TestStreamConn(t *testing.T) {
	nettest.TestConn(t, func() (c1, c2 net.Conn, stop func(), err error) {
		ctx, stop := context.WithCancel(dlog.WithLogger(context.Background(), log.NewTestLogger(t, dlog.LogLevelDebug)))
		tunnel := newBidi(1, ctx)
		localAddr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001)
		remoteAddr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 8080)
		id := NewConnID(types.ProtoTCP, localAddr, remoteAddr)
		si := SessionID(uuid.New().String())

		g, _ := errgroup.WithContext(ctx)
		g.Go(func() error {
			client, err := NewClientStream(ctx, ClientToManager, tunnel.clientSide(), id, si, 0, 0)
			if err != nil {
				return err
			}
			assert.Equal(t, Version, client.PeerVersion())
			c1 = NewStreamConn(ctx, client, nil, nil)
			return nil
		})
		g.Go(func() error {
			server, err := NewServerStream(ctx, ManagerToClient, tunnel.serverSide())
			if err != nil {
				return err
			}
			assert.Equal(t, Version, server.PeerVersion())
			assert.Equal(t, si, server.SessionID())
			c2 = NewStreamConn(ctx, server, nil, nil)
			return nil
		})
		err = g.Wait()
		if err != nil {
			stop()
			return nil, nil, nil, err
		}
		return c1, c2, stop, nil
	})
}
