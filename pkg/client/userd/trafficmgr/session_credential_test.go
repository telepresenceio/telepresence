package trafficmgr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestSessionCredentialCacheFetchesOnce(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := &sessionCredentialCache{}
	fetch := func(context.Context) (*manager.SessionCredential, error) {
		calls++
		return &manager.SessionCredential{
			Token:  "tok-1",
			Expiry: timestamppb.New(time.Now().Add(time.Hour)),
		}, nil
	}

	cred := c.get(ctx, fetch)
	require.NotNil(t, cred)
	assert.Equal(t, "tok-1", cred.Token)
	assert.Equal(t, 1, calls)

	// A second call within the credential's lifetime uses the cache.
	cred = c.get(ctx, fetch)
	require.NotNil(t, cred)
	assert.Equal(t, "tok-1", cred.Token)
	assert.Equal(t, 1, calls)
}

func TestSessionCredentialCacheRefetchesNearExpiry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := &sessionCredentialCache{
		cred: &manager.SessionCredential{
			Token: "stale",
			// Within the expiry margin: due for a refresh.
			Expiry: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	fetch := func(context.Context) (*manager.SessionCredential, error) {
		calls++
		return &manager.SessionCredential{
			Token:  "fresh",
			Expiry: timestamppb.New(time.Now().Add(time.Hour)),
		}, nil
	}

	cred := c.get(ctx, fetch)
	require.NotNil(t, cred)
	assert.Equal(t, "fresh", cred.Token)
	assert.Equal(t, 1, calls)
}

func TestSessionCredentialCacheUnimplementedSticks(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := &sessionCredentialCache{}
	fetch := func(context.Context) (*manager.SessionCredential, error) {
		calls++
		return nil, status.Error(codes.Unimplemented, "GetSessionCredential not implemented")
	}

	cred := c.get(ctx, fetch)
	assert.Nil(t, cred)
	assert.Equal(t, 1, calls)

	// Once marked unsupported, fetch is never called again.
	cred = c.get(ctx, fetch)
	assert.Nil(t, cred)
	assert.Equal(t, 1, calls)
}

func TestSessionCredentialCacheOtherErrorRetriesEveryCall(t *testing.T) {
	ctx := context.Background()
	calls := 0
	c := &sessionCredentialCache{}
	fetch := func(context.Context) (*manager.SessionCredential, error) {
		calls++
		return nil, status.Error(codes.Unavailable, "manager unreachable")
	}

	cred := c.get(ctx, fetch)
	assert.Nil(t, cred)
	assert.Equal(t, 1, calls)

	// Not marked unsupported, so a later call retries the fetch.
	cred = c.get(ctx, fetch)
	assert.Nil(t, cred)
	assert.Equal(t, 2, calls)
}
