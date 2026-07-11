//go:build linux

package cri

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// fakeRuntimeService implements only ContainerStatus, recording the request
// it received and returning a configurable response or error.
type fakeRuntimeService struct {
	runtimeapi.UnimplementedRuntimeServiceServer

	gotContainerID string
	gotVerbose     bool

	resp *runtimeapi.ContainerStatusResponse
	err  error

	// connCount counts accepted connections to the fake server's listener.
	connCount atomic.Int32
}

// countingListener wraps a net.Listener and counts accepted connections, so
// tests can observe whether a Client's calls share one connection.
type countingListener struct {
	net.Listener
	count *atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.count.Add(1)
	}
	return conn, err
}

func (f *fakeRuntimeService) ContainerStatus(
	_ context.Context,
	req *runtimeapi.ContainerStatusRequest,
) (*runtimeapi.ContainerStatusResponse, error) {
	f.gotContainerID = req.GetContainerId()
	f.gotVerbose = req.GetVerbose()
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// startFakeCRI starts a gRPC server on a unix socket and returns the socket
// path. The socket is created under a dedicated temp directory with a short
// filename because t.TempDir() paths can exceed the ~108 byte unix sockaddr
// limit.
func startFakeCRI(t *testing.T, svc *fakeRuntimeService) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cri")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "c.sock")
	lis, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	lis = &countingListener{Listener: lis, count: &svc.connCount}

	srv := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return sockPath
}

// connect dials sockPath and registers the returned Client's Close for
// cleanup.
func connect(t *testing.T, sockPath string) *Client {
	t.Helper()
	c, err := Connect(sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestResolvePID_HappyPath(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 4242}`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	pid, err := c.ResolvePID(context.Background(), "abc123")
	require.NoError(t, err)
	require.Equal(t, 4242, pid)
	require.True(t, svc.gotVerbose)
	require.Equal(t, "abc123", svc.gotContainerID)
}

func TestResolvePID_StripsRuntimePrefix(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 99}`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	pid, err := c.ResolvePID(context.Background(), "containerd://abc123")
	require.NoError(t, err)
	require.Equal(t, 99, pid)
	require.Equal(t, "abc123", svc.gotContainerID)
}

func TestResolvePID_MissingInfoKey(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	_, err := c.ResolvePID(context.Background(), "abc123")
	require.ErrorContains(t, err, "abc123")
	require.ErrorContains(t, err, "info")
}

func TestResolvePID_InvalidJSON(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `not json`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	_, err := c.ResolvePID(context.Background(), "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_NoPid(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"other": 1}`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	_, err := c.ResolvePID(context.Background(), "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_ZeroPid(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 0}`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	_, err := c.ResolvePID(context.Background(), "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_NoSuchSocket(t *testing.T) {
	dir := t.TempDir()
	c := connect(t, filepath.Join(dir, "no.sock"))
	_, err := c.ResolvePID(context.Background(), "abc123")
	require.Error(t, err)
}

// TestResolvePID_ReusesConnection verifies that resolving several
// containers' PIDs through the same Client dials the CRI socket once.
func TestResolvePID_ReusesConnection(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 4242}`},
		},
	}
	sockPath := startFakeCRI(t, svc)
	c := connect(t, sockPath)

	_, err := c.ResolvePID(context.Background(), "abc123")
	require.NoError(t, err)
	_, err = c.ResolvePID(context.Background(), "def456")
	require.NoError(t, err)

	require.Equal(t, int32(1), svc.connCount.Load())
}

func TestStripRuntimePrefix(t *testing.T) {
	require.Equal(t, "abc123", StripRuntimePrefix("containerd://abc123"))
	require.Equal(t, "abc123", StripRuntimePrefix("abc123"))
	require.Equal(t, "", StripRuntimePrefix(""))
}

// serveFakeCRI starts a gRPC server for svc on the socket at path, creating
// parent directories as needed. A nil svc registers no services at all, so
// every CRI call answers Unimplemented.
func serveFakeCRI(t *testing.T, path string, svc runtimeapi.RuntimeServiceServer) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	lis, err := net.Listen("unix", path)
	require.NoError(t, err)
	srv := grpc.NewServer()
	if svc != nil {
		runtimeapi.RegisterRuntimeServiceServer(srv, svc)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
}

// TestSocketFor exercises the docker-runtime layout that defeats liveness
// probing: a containerd socket serving a fully functional CRI that does not
// know the container (minikube's docker-runtime node runs one), a plain file
// where a socket is expected, and cri-dockerd holding the actual container.
// Detection must pick the socket that recognizes the container, whatever the
// container ID's runtime scheme suggests.
func TestSocketFor(t *testing.T) {
	root, err := os.MkdirTemp("", "cri")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	// A live CRI on the containerd socket that knows no containers.
	notFound := status.Error(codes.NotFound, "no such container")
	serveFakeCRI(t, filepath.Join(root, "containerd", "containerd.sock"), &fakeRuntimeService{err: notFound})

	// A regular file where a socket is expected must be skipped outright.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "crio"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "crio", "crio.sock"), nil, 0o600))

	// No socket recognizes the container yet.
	_, err = SocketFor(context.Background(), root, "docker://abc123")
	require.Error(t, err)

	// cri-dockerd knows the container; it must win for the docker:// scheme
	// and, via fallback probing, for an unknown scheme as well.
	serveFakeCRI(t, filepath.Join(root, "cri-dockerd.sock"), &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{},
	})

	socket, err := SocketFor(context.Background(), root, "docker://abc123")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "cri-dockerd.sock"), socket)

	socket, err = SocketFor(context.Background(), root, "strange://abc123")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "cri-dockerd.sock"), socket)
}
