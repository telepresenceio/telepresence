// +build !windows

package client_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestDialSocket(t *testing.T) {
	tmpdir := t.TempDir()
	t.Run("OK", func(t *testing.T) {
		sockname := filepath.Join(tmpdir, "ok.sock")
		listener, err := net.Listen("unix", sockname)
		if !assert.NoError(t, err) {
			return
		}
		defer listener.Close()

		ctx := dlog.NewTestContext(t, false)
		grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
			EnableWithSoftness: true,
			ShutdownOnNonError: true,
			DisableLogging:     true,
		})

		grp.Go("server", func(ctx context.Context) error {
			sc := &dhttp.ServerConfig{
				Handler: grpc.NewServer(),
			}
			return sc.Serve(ctx, listener)
		})

		grp.Go("client", func(ctx context.Context) error {
			conn, err := client.DialSocket(ctx, sockname)
			assert.NoError(t, err)
			if assert.NotNil(t, conn) {
				assert.NoError(t, conn.Close())
			}
			return nil
		})

		assert.NoError(t, grp.Wait())
	})
	t.Run("Hang", func(t *testing.T) {
		sockname := filepath.Join(tmpdir, "hang.sock")
		listener, err := net.Listen("unix", sockname)
		if !assert.NoError(t, err) {
			return
		}
		defer listener.Close()

		ctx := dlog.NewTestContext(t, false)
		conn, err := client.DialSocket(ctx, sockname)
		assert.Nil(t, conn)
		assert.Error(t, err)
		t.Log(err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "dial unix "+sockname)
		assert.Contains(t, err.Error(), "this usually means that the process has locked up")
	})
	t.Run("Orphan", func(t *testing.T) {
		sockname := filepath.Join(tmpdir, "orphan.sock")
		listener, err := net.Listen("unix", sockname)
		if !assert.NoError(t, err) {
			return
		}
		listener.(*net.UnixListener).SetUnlinkOnClose(false)
		listener.Close()

		ctx := dlog.NewTestContext(t, false)
		conn, err := client.DialSocket(ctx, sockname)
		assert.Nil(t, conn)
		assert.Error(t, err)
		t.Log(err)
		assert.ErrorIs(t, err, syscall.ECONNREFUSED)
		assert.Contains(t, err.Error(), "dial unix "+sockname)
		assert.Contains(t, err.Error(), "this usually means that the process has terminated ungracefully")
	})
	t.Run("NotExist", func(t *testing.T) {
		ctx := dlog.NewTestContext(t, false)
		sockname := filepath.Join(tmpdir, "not-exist.sock")
		conn, err := client.DialSocket(ctx, sockname)
		assert.Nil(t, conn)
		assert.Error(t, err)
		t.Log(err)
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.Contains(t, err.Error(), "dial unix "+sockname)
		assert.Contains(t, err.Error(), "this usually means that the process is not running")
	})
}
