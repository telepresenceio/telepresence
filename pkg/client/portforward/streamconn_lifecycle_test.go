package portforward

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type pausedCloseConnection struct {
	httpstream.Connection
	failFirst    bool
	calls        atomic.Int32
	closeStarted chan struct{}
	allowClose   chan struct{}
	closeOnce    sync.Once
}

func (c *pausedCloseConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	if c.calls.Add(1) == 1 && c.failFirst {
		return nil, errors.New("injected stream rejection")
	}
	return c.Connection.CreateStream(headers)
}

func (c *pausedCloseConnection) Close() error {
	c.closeOnce.Do(func() { close(c.closeStarted) })
	<-c.allowClose
	return c.Connection.Close()
}

// A cluster cannot reliably pause teardown while exactly one new dial acquires
// the same cached transport, so the test controls that schedule locally.
func Test_RetiredDialerRetriesWithoutClosingNewConnection(t *testing.T) {
	for _, failedDial := range []bool{true, false} {
		name := "last connection closes"
		if failedDial {
			name = "first stream fails"
		}
		t.Run(name, func(t *testing.T) {
			echo := startEcho(t)
			server := startPortForwardServer(t, map[int32]string{2222: echo})
			clientConfig := client.GetDefaultConfig()
			clientConfig.Cluster().ForceSPDY = true
			ctx := WithRestConfig(client.WithConfig(t.Context(), clientConfig), &rest.Config{Host: server.URL})
			cfg := ctx.Value(dialerKey{}).(*config)
			pd, err := newPodDialer(ctx, "echo.default", cfg, "echo", "default")
			require.NoError(t, err)
			cfg.podDialers.Store("echo.default", pd)
			var initial net.Conn
			if !failedDial {
				initial, err = pd.dial(ctx, 2222)
				require.NoError(t, err)
			}
			wrapped := &pausedCloseConnection{
				Connection: pd.streamConn, failFirst: failedDial,
				closeStarted: make(chan struct{}), allowClose: make(chan struct{}),
			}
			pd.streamConn = wrapped
			var unblock sync.Once
			defer unblock.Do(func() { close(wrapped.allowClose) })
			done := make(chan error, 1)
			go func() {
				if failedDial {
					_, err := pd.dial(ctx, 2222)
					done <- err
				} else {
					done <- initial.Close()
				}
			}()
			select {
			case <-wrapped.closeStarted:
			case <-time.After(10 * time.Second):
				t.Fatal("transport cleanup did not start")
			}

			conn, err := Dialer(ctx)(ctx, "pod/echo.default:2222")
			require.NoError(t, err)
			defer func() {
				unblock.Do(func() { close(wrapped.allowClose) })
				_ = conn.Close()
			}()
			require.NotSame(t, pd, conn.(*portConn).dialer)
			step(t, "new connection before old cleanup", 10*time.Second, func() error {
				return roundtrip(conn, "before-cleanup")
			})
			unblock.Do(func() { close(wrapped.allowClose) })
			if failedDial {
				require.ErrorContains(t, <-done, "injected stream rejection")
			} else {
				require.NoError(t, <-done)
			}
			cached, ok := cfg.podDialers.Load("echo.default")
			require.True(t, ok, "old cleanup must preserve the new cache entry")
			require.Same(t, conn.(*portConn).dialer, cached)
			step(t, "new connection after old cleanup", 10*time.Second, func() error {
				return roundtrip(conn, "after-cleanup")
			})
		})
	}
}

// A cluster cannot force a consumer to close the same net.Conn twice.
func Test_RepeatedCloseKeepsSiblingsHealthy(t *testing.T) {
	pd, short, longPing := sharedConnFixture(t)
	require.NoError(t, short.Close())
	require.NoError(t, short.Close())
	assertSiblingsHealthy(t, pd, longPing)
}
