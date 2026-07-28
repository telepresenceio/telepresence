package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestMintedTokens_MintAndLookup(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	p := &auth.Principal{Username: "u", Groups: []string{"g"}}

	token, expiresAt, err := m.Mint(p, "fp-1", time.Now().Add(24*time.Hour), 1)
	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, time.Minute)

	got, ok := m.Lookup(token)
	require.True(t, ok)
	assert.Same(t, p, got)
}

func TestMintedTokens_UnknownToken(t *testing.T) {
	m := auth.NewMintedTokens()
	p, ok := m.Lookup("nope")
	assert.False(t, ok)
	assert.Nil(t, p)
}

func TestMintedTokens_DistinctTokens(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	t1, _, err := m.Mint(&auth.Principal{Username: "a"}, "fp-a", time.Now().Add(24*time.Hour), 1)
	require.NoError(t, err)
	t2, _, err := m.Mint(&auth.Principal{Username: "b"}, "fp-b", time.Now().Add(24*time.Hour), 1)
	require.NoError(t, err)
	assert.NotEqual(t, t1, t2)

	p1, ok := m.Lookup(t1)
	require.True(t, ok)
	assert.Equal(t, "a", p1.Username)

	p2, ok := m.Lookup(t2)
	require.True(t, ok)
	assert.Equal(t, "b", p2.Username)
}

// TestMintedTokens_ExpiryClampedToCertNotAfter verifies that a certificate expiring
// sooner than mintedTokenTTL produces a token that expires with the certificate rather
// than at the usual TTL.
func TestMintedTokens_ExpiryClampedToCertNotAfter(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	notAfter := time.Now().Add(5 * time.Minute)

	_, expiresAt, err := m.Mint(&auth.Principal{Username: "u"}, "fp-1", notAfter, 1)
	require.NoError(t, err)
	assert.WithinDuration(t, notAfter, expiresAt, time.Second)
}

// TestMintedTokens_FingerprintReuseReturnsSameToken verifies that minting again for the
// same certificate fingerprint, before the previous token expires, returns that token
// instead of creating a new entry.
func TestMintedTokens_FingerprintReuseReturnsSameToken(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	notAfter := time.Now().Add(24 * time.Hour)

	t1, exp1, err := m.Mint(&auth.Principal{Username: "u"}, "fp-1", notAfter, 1)
	require.NoError(t, err)

	t2, exp2, err := m.Mint(&auth.Principal{Username: "u"}, "fp-1", notAfter, 1)
	require.NoError(t, err)

	assert.Equal(t, t1, t2)
	assert.Equal(t, exp1, exp2)
}

// TestMintedTokens_InvalidateAll verifies that a token minted before InvalidateAll no
// longer resolves afterward.
func TestMintedTokens_InvalidateAll(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	token, _, err := m.Mint(&auth.Principal{Username: "u"}, "fp-1", time.Now().Add(24*time.Hour), 1)
	require.NoError(t, err)

	m.InvalidateAll(2)

	_, ok := m.Lookup(token)
	assert.False(t, ok)
}

// TestMintedTokens_InvalidateAllMonotonic verifies that InvalidateAll ignores a
// generation that isn't strictly newer than the store's current one, so that two calls
// racing each other -- e.g. from two overlapping reloads -- can never regress the store
// to an older generation.
func TestMintedTokens_InvalidateAllMonotonic(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(3)
	notAfter := time.Now().Add(24 * time.Hour)

	token, _, err := m.Mint(&auth.Principal{Username: "u"}, "fp-1", notAfter, 3)
	require.NoError(t, err)

	// A stale InvalidateAll(2) arriving after InvalidateAll(3) must be ignored
	// entirely: it neither clears the store nor regresses its generation.
	m.InvalidateAll(2)

	_, ok := m.Lookup(token)
	assert.True(t, ok, "InvalidateAll(2) after InvalidateAll(3) must not clear the store")

	token2, _, err := m.Mint(&auth.Principal{Username: "u2"}, "fp-2", notAfter, 3)
	require.NoError(t, err)
	assert.NotEmpty(t, token2, "minting against generation 3 must still work")

	_, _, err = m.Mint(&auth.Principal{Username: "u"}, "fp-1", notAfter, 2)
	assert.ErrorIs(t, err, auth.ErrCAGenerationMismatch, "the store's generation must still be 3, not 2")
}

// TestMintedTokens_MintRefusedWithoutMatchingGeneration verifies that Mint refuses --
// with ErrCAGenerationMismatch -- both a caGeneration of 0 (no CA ever loaded) and one
// that doesn't match the store's current CA generation, and succeeds once given the
// generation InvalidateAll most recently recorded.
func TestMintedTokens_MintRefusedWithoutMatchingGeneration(t *testing.T) {
	m := auth.NewMintedTokens()
	p := &auth.Principal{Username: "u"}
	notAfter := time.Now().Add(24 * time.Hour)

	_, _, err := m.Mint(p, "fp-1", notAfter, 0)
	assert.ErrorIs(t, err, auth.ErrCAGenerationMismatch)

	m.InvalidateAll(1)
	_, _, err = m.Mint(p, "fp-1", notAfter, 2)
	assert.ErrorIs(t, err, auth.ErrCAGenerationMismatch)

	token, _, err := m.Mint(p, "fp-1", notAfter, 1)
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestMintedTokens_NearExpiryTokenNotReused(t *testing.T) {
	m := auth.NewMintedTokens()
	m.InvalidateAll(1)
	p := &auth.Principal{Username: "u"}

	// A token whose remaining validity is inside the reuse margin must be
	// replaced by a fresh one, or a client refreshing near expiry would be
	// handed the same near-expired token back on every handshake.
	tok1, _, err := m.Mint(p, "fp-1", time.Now().Add(30*time.Second), 1)
	require.NoError(t, err)
	tok2, _, err := m.Mint(p, "fp-1", time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	assert.NotEqual(t, tok1, tok2)

	// The replaced token stays valid until its own expiry, so calls already
	// carrying it keep working.
	_, ok := m.Lookup(tok1)
	assert.True(t, ok)
	_, ok = m.Lookup(tok2)
	assert.True(t, ok)

	// A token with validity well beyond the margin is reused.
	tok3, _, err := m.Mint(p, "fp-1", time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	assert.Equal(t, tok2, tok3)
}
