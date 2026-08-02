package agentpf

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
)

// TestTokenCredentials_NilProvider covers the default (no session credential fetched
// yet, or agentpf.NewClients was passed nil): no metadata is attached at all.
func TestTokenCredentials_NilProvider(t *testing.T) {
	c := tokenCredentials{}
	md, err := c.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Nil(t, md)
	assert.False(t, c.RequireTransportSecurity())
}

// TestTokenCredentials_ProviderReturnsEmpty covers a provider that has no token yet
// (e.g. the manager doesn't implement GetSessionCredential): still no metadata.
func TestTokenCredentials_ProviderReturnsEmpty(t *testing.T) {
	c := tokenCredentials{provider: func(context.Context) string { return "" }}
	md, err := c.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Nil(t, md)
}

// TestTokenCredentials_NonEmptyToken covers the populated case: the token is attached
// under sessiontoken.MetadataKey.
func TestTokenCredentials_NonEmptyToken(t *testing.T) {
	c := tokenCredentials{provider: func(context.Context) string { return "tok-1" }}
	md, err := c.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{sessiontoken.MetadataKey: "tok-1"}, md)
}
