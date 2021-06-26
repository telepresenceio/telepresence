package client_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestDialSocket(t *testing.T) {
	t.Parallel()
	tmpdir := t.TempDir()
	t.Run("OK", func(t *testing.T) {
		t.Parallel()
		listener, err := net.Listen("unix", filepath.Join(tmpdir, "ok.sock"))
		if !assert.NoError(t, err) {
			return
		}
		defer listener.Close()

		ctx := dlog.NewTestContext(t, false)
		grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
			EnableWithSoftness: true,
			ShutdownOnNonError: true,
		})

		grp.Go("server", func(ctx context.Context) error {
			sc := &dhttp.ServerConfig{
				Handler: grpc.NewServer(),
			}
			return sc.Serve(ctx, listener)
		})

		grp.Go("client", func(ctx context.Context) error {
			conn, err := client.DialSocket(ctx, filepath.Join(tmpdir, "ok.sock"))
			assert.NoError(t, err)
			if assert.NotNil(t, conn) {
				assert.NoError(t, conn.Close())
			}
			return nil
		})

		assert.NoError(t, grp.Wait())
	})
	t.Run("Hang", func(t *testing.T) {
		t.Parallel()
		listener, err := net.Listen("unix", filepath.Join(tmpdir, "hang.sock"))
		if !assert.NoError(t, err) {
			return
		}
		defer listener.Close()

		ctx := dlog.NewTestContext(t, false)
		conn, err := client.DialSocket(ctx, filepath.Join(tmpdir, "hang.sock"))
		assert.Nil(t, conn)
		assert.Error(t, err)
		t.Log(err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("NotExist", func(t *testing.T) {
		t.Parallel()
		ctx := dlog.NewTestContext(t, false)
		conn, err := client.DialSocket(ctx, filepath.Join(tmpdir, "not-exist.sock"))
		assert.Nil(t, conn)
		assert.Error(t, err)
		t.Log(err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
}
