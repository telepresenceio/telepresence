package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/systema"
)

type server struct {
	manager.UnimplementedManagerServer
}

func (s server) ListIntercepts(ctx context.Context, _ *empty.Empty) (*manager.InterceptInfoSnapshot, error) {
	return nil, nil
}
func (s server) RemoveIntercept(context.Context, *manager.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, nil
}
func (s server) DialIntercept(ctx context.Context, interceptID string) (net.Conn, error) {
	fmt.Printf("INTERCEPT_ID: %q\n", interceptID)
	return stdioConn{}, nil
}

type stdioConn struct{}

func (stdioConn) Read(b []byte) (n int, err error)   { return os.Stdin.Read(b) }
func (stdioConn) Write(b []byte) (n int, err error)  { return os.Stdout.Write(b) }
func (stdioConn) Close() error                       { return nil }
func (stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }
func (stdioConn) SetDeadline(t time.Time) error      { return nil }
func (stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (stdioConn) SetWriteDeadline(t time.Time) error { return nil }

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

func main() {
	grp := dgroup.NewGroup(context.Background(), dgroup.GroupConfig{
		EnableSignalHandling: true,
	})
	grp.Go("main", func(ctx context.Context) error {
		// credBundle := TODO
		_, wait, err := systema.ConnectToSystemA(ctx, server{}, "localhost:8000",
			grpc.WithInsecure(),
			// grpc.WithCredentialsBundle(credBundle),
		)
		if err != nil {
			return err
		}
		return wait()
	})
	if err := grp.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
}
