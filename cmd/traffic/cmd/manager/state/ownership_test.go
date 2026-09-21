package state

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func TestClientOwnershipError(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{})
	g := log.NewGroup(ctx)
	p := NewState(ctx, g, nil)

	alice := &auth.Principal{Username: "alice", UID: "alice-uid"}
	now := time.Now()
	sessionID := p.AddClient(&rpc.ClientInfo{Name: "alice-client"}, alice, now)
	cs := p.GetClient(sessionID)
	require.NotNil(t, cs)

	t.Run("matching principal", func(t *testing.T) {
		ctx := auth.WithPrincipal(context.Background(), alice)
		assert.NoError(t, ClientOwnershipError(ctx, sessionID, cs))
	})

	t.Run("matching session credential", func(t *testing.T) {
		ctx := auth.WithSessionCredential(context.Background(), string(sessionID))
		assert.NoError(t, ClientOwnershipError(ctx, sessionID, cs))
	})

	t.Run("session credential for a different session", func(t *testing.T) {
		ctx := auth.WithSessionCredential(context.Background(), "some-other-session")
		err := ClientOwnershipError(ctx, sessionID, cs)
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("no credential and no principal", func(t *testing.T) {
		err := ClientOwnershipError(context.Background(), sessionID, cs)
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("different principal", func(t *testing.T) {
		bob := &auth.Principal{Username: "bob", UID: "bob-uid"}
		ctx := auth.WithPrincipal(context.Background(), bob)
		err := ClientOwnershipError(ctx, sessionID, cs)
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("unowned session", func(t *testing.T) {
		unownedID := p.AddClient(&rpc.ClientInfo{Name: "no-principal-client"}, nil, now)
		unowned := p.GetClient(unownedID)
		require.NotNil(t, unowned)
		assert.NoError(t, ClientOwnershipError(context.Background(), unownedID, unowned))
	})
}
