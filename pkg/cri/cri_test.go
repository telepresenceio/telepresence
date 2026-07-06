//go:build linux

package cri

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

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

	srv := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return sockPath
}

func TestResolvePID_HappyPath(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 4242}`},
		},
	}
	sockPath := startFakeCRI(t, svc)

	pid, err := ResolvePID(context.Background(), sockPath, "abc123")
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

	pid, err := ResolvePID(context.Background(), sockPath, "containerd://abc123")
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

	_, err := ResolvePID(context.Background(), sockPath, "abc123")
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

	_, err := ResolvePID(context.Background(), sockPath, "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_NoPid(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"other": 1}`},
		},
	}
	sockPath := startFakeCRI(t, svc)

	_, err := ResolvePID(context.Background(), sockPath, "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_ZeroPid(t *testing.T) {
	svc := &fakeRuntimeService{
		resp: &runtimeapi.ContainerStatusResponse{
			Info: map[string]string{"info": `{"pid": 0}`},
		},
	}
	sockPath := startFakeCRI(t, svc)

	_, err := ResolvePID(context.Background(), sockPath, "abc123")
	require.ErrorContains(t, err, "abc123")
}

func TestResolvePID_NoSuchSocket(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolvePID(context.Background(), filepath.Join(dir, "no.sock"), "abc123")
	require.Error(t, err)
}

func TestStripRuntimePrefix(t *testing.T) {
	require.Equal(t, "abc123", StripRuntimePrefix("containerd://abc123"))
	require.Equal(t, "abc123", StripRuntimePrefix("abc123"))
	require.Equal(t, "", StripRuntimePrefix(""))
}
