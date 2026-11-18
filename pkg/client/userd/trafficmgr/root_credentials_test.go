package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

// credentialTestServer is a root daemon that records the credential it is given in Connect and,
// once connected, asks for credentials on the stream.
type credentialTestServer struct {
	rootDaemonReconnectTestServer

	requests int

	mu          sync.Mutex
	connectCred []string
	replies     chan *rootdRpc.CredentialReply
}

func (s *credentialTestServer) Connect(ctx context.Context, nc *rootdRpc.NetworkConfig) (*rootdRpc.DaemonStatus, error) {
	s.mu.Lock()
	s.connectCred = append(s.connectCred, string(nc.ExecCredential))
	s.mu.Unlock()
	return s.rootDaemonReconnectTestServer.Connect(ctx, nc)
}

func (s *credentialTestServer) ProvideCredentials(stream grpc.BidiStreamingServer[rootdRpc.CredentialReply, rootdRpc.CredentialRequest]) error {
	for i := range s.requests {
		if err := stream.Send(&rootdRpc.CredentialRequest{Id: uint64(i + 1)}); err != nil {
			return err
		}
	}
	for range s.requests {
		r, err := stream.Recv()
		if err != nil {
			return err
		}
		s.replies <- r
	}
	<-stream.Context().Done()
	return nil
}

func newCredentialTestSession(ctx context.Context, sessionID string, run func(context.Context, *clientcmdapi.ExecConfig) ([]byte, error), dial func(context.Context, bool) (*grpc.ClientConn, error)) *session {
	s := &session{
		Cluster: &k8s.Cluster{
			Kubeconfig: &k8s.Kubeconfig{Context: ctx, Namespace: "default", KubeContext: "test", Server: "https://cluster.example"},
		},
		service:        rootDaemonReconnectTestService{},
		sessionInfo:    &manager.SessionInfo{SessionId: sessionID},
		dialRootDaemon: dial,
		rootCreds:      newRootCredentials(&clientcmdapi.ExecConfig{Command: "plugin"}),
	}
	s.rootCreds.run = run
	return s
}

func TestRootCredentials_StreamAnswersOverlappingRequests(t *testing.T) {
	const (
		sessionID = "test-session"
		requests  = 3
	)
	ctx, cancel := context.WithCancel(client.WithConfig(context.Background(), client.GetDefaultConfig()))
	defer cancel()

	srv := &credentialTestServer{
		rootDaemonReconnectTestServer: rootDaemonReconnectTestServer{name: "first", sessionID: sessionID},
		requests:                      requests,
		replies:                       make(chan *rootdRpc.CredentialReply, requests),
	}

	// The plugin only returns once all requests are running, which proves that they are served concurrently.
	var running atomic.Int32
	var runs atomic.Int32
	allRunning := make(chan struct{})
	run := func(ctx context.Context, ec *clientcmdapi.ExecConfig) ([]byte, error) {
		n := runs.Add(1)
		if running.Add(1) == requests {
			close(allRunning)
		}
		select {
		case <-allRunning:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if n == 1 {
			return nil, errors.New("plugin failed")
		}
		return fmt.Appendf(nil, "cred-%d", n), nil
	}
	conn, cleanup, err := dialTestRootDaemon(srv)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	s := newCredentialTestSession(ctx, sessionID, run, func(context.Context, bool) (*grpc.ClientConn, error) { return conn, nil })

	nc := &rootdRpc.NetworkConfig{Namespace: "default", Session: s.sessionInfo, ExecCredential: []byte("first")}
	require.NoError(t, s.connectRootDaemon(ctx, nc, nil, false))

	got := make(map[uint64]*rootdRpc.CredentialReply)
	for range requests {
		select {
		case r := <-srv.replies:
			got[r.Id] = r
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for credential replies")
		}
	}
	var failed, succeeded int
	for id, r := range got {
		if r.Error != "" {
			failed++
			require.Equal(t, "plugin failed", r.Error)
			require.Empty(t, r.ExecCredential)
		} else {
			succeeded++
			require.Regexp(t, `^cred-\d+$`, string(r.ExecCredential), "reply %d", id)
		}
	}
	require.Equal(t, 1, failed)
	require.Equal(t, requests-1, succeeded)
	require.Equal(t, []string{"first"}, srv.connectCred)
}

func TestRootCredentials_NoStreamWithoutCredential(t *testing.T) {
	const sessionID = "test-session"
	ctx, cancel := context.WithCancel(client.WithConfig(context.Background(), client.GetDefaultConfig()))
	defer cancel()

	var opened atomic.Bool
	srv := &streamCountingServer{rootDaemonReconnectTestServer: rootDaemonReconnectTestServer{name: "first", sessionID: sessionID}, opened: &opened}
	conn, cleanup, err := dialTestRootDaemon(srv)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	s := newCredentialTestSession(ctx, sessionID, nil, func(context.Context, bool) (*grpc.ClientConn, error) { return conn, nil })
	s.rootCreds = nil
	nc := &rootdRpc.NetworkConfig{Namespace: "default", Session: s.sessionInfo}
	require.NoError(t, s.connectRootDaemon(ctx, nc, nil, false))
	time.Sleep(100 * time.Millisecond)
	require.False(t, opened.Load())
}

type streamCountingServer struct {
	rootDaemonReconnectTestServer
	opened *atomic.Bool
}

func (s *streamCountingServer) ProvideCredentials(grpc.BidiStreamingServer[rootdRpc.CredentialReply, rootdRpc.CredentialRequest]) error {
	s.opened.Store(true)
	return status.Error(codes.Unimplemented, "unexpected")
}

func TestRootCredentials_ReconnectRefreshesTheFirstCredential(t *testing.T) {
	const sessionID = "test-session"
	ctx, cancel := context.WithCancel(client.WithConfig(context.Background(), client.GetDefaultConfig()))
	defer cancel()

	first := &credentialTestServer{
		rootDaemonReconnectTestServer: rootDaemonReconnectTestServer{
			name:        "first",
			sessionID:   sessionID,
			activityErr: status.Error(codes.Unavailable, "rootd exited"),
		},
		replies: make(chan *rootdRpc.CredentialReply, 1),
	}
	second := &credentialTestServer{
		rootDaemonReconnectTestServer: rootDaemonReconnectTestServer{name: "second", sessionID: sessionID},
		replies:                       make(chan *rootdRpc.CredentialReply, 1),
	}
	servers := []*credentialTestServer{first, second}
	var dials atomic.Int32
	dial := func(context.Context, bool) (*grpc.ClientConn, error) {
		idx := int(dials.Add(1)) - 1
		if idx >= len(servers) {
			return nil, fmt.Errorf("unexpected root daemon dial %d", idx+1)
		}
		conn, cleanup, err := dialTestRootDaemon(servers[idx])
		if err != nil {
			return nil, err
		}
		t.Cleanup(cleanup)
		return conn, nil
	}
	var runs atomic.Int32
	run := func(context.Context, *clientcmdapi.ExecConfig) ([]byte, error) {
		return fmt.Appendf(nil, "fresh-%d", runs.Add(1)), nil
	}
	s := newCredentialTestSession(ctx, sessionID, run, dial)
	nc := &rootdRpc.NetworkConfig{Namespace: "default", Session: s.sessionInfo, ExecCredential: []byte("first")}
	require.NoError(t, s.connectRootDaemon(ctx, nc, nil, false))

	require.Eventually(t, func() bool {
		second.mu.Lock()
		defer second.mu.Unlock()
		return len(second.connectCred) == 1
	}, 5*time.Second, 10*time.Millisecond)
	second.mu.Lock()
	defer second.mu.Unlock()
	require.Equal(t, []string{"fresh-1"}, second.connectCred)
}
