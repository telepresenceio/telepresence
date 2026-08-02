package trafficmgr

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// fakeSessionCredentialManager is a minimal manager.ManagerServer stand-in that answers
// GetSessionCredential and counts calls, so the test below can assert that
// session.SessionCredential both delegates to, and is cached by, the sessioncred.Cache
// it now wraps.
type fakeSessionCredentialManager struct {
	manager.UnimplementedManagerServer
	calls int
}

func (f *fakeSessionCredentialManager) GetSessionCredential(
	context.Context, *manager.SessionInfo,
) (*manager.SessionCredential, error) {
	f.calls++
	return &manager.SessionCredential{
		Token:  "tok-1",
		Expiry: timestamppb.New(time.Now().Add(time.Hour)),
	}, nil
}

// TestSessionCredentialDelegatesToCache covers session.SessionCredential's contract
// after the move to sessioncred.Cache: it fetches through the manager client on first
// use and reuses the cached credential for a later call within its lifetime.
func TestSessionCredentialDelegatesToCache(t *testing.T) {
	ctx := context.Background()
	fm := &fakeSessionCredentialManager{}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := grpc.NewServer()
	manager.RegisterManagerServer(svc, fm)
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	s := &session{managerConn: conn, sessionInfo: &manager.SessionInfo{SessionId: "session-a"}}

	cred := s.SessionCredential(ctx)
	require.NotNil(t, cred)
	require.Equal(t, "tok-1", cred.Token)
	require.Equal(t, 1, fm.calls)

	// A second call within the credential's lifetime is served from the cache.
	cred = s.SessionCredential(ctx)
	require.NotNil(t, cred)
	require.Equal(t, "tok-1", cred.Token)
	require.Equal(t, 1, fm.calls)
}
