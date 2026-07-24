package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMintedTokens_ExpiredEntryEvicted reaches into the store directly to plant an
// already-expired entry, since mintedTokenTTL is not configurable from outside the
// package.
func TestMintedTokens_ExpiredEntryEvicted(t *testing.T) {
	m := NewMintedTokens()
	p := &Principal{Username: "u"}

	m.mu.Lock()
	m.tokens["expired"] = &mintedTokenEntry{token: "expired", principal: p, fingerprint: "fp", expiresAt: time.Now().Add(-time.Minute)}
	m.byFP["fp"] = m.tokens["expired"]
	m.mu.Unlock()

	got, ok := m.Lookup("expired")
	assert.False(t, ok)
	assert.Nil(t, got)

	m.mu.Lock()
	_, stillPresent := m.tokens["expired"]
	m.mu.Unlock()
	assert.False(t, stillPresent, "an expired entry must be evicted on lookup")
}

// TestMintedTokens_CapEnforced verifies that minting for more than mintedTokenMaxEntries
// distinct certificates never grows the store past the cap, evicting the
// soonest-to-expire entry to make room.
func TestMintedTokens_CapEnforced(t *testing.T) {
	m := NewMintedTokens()
	m.InvalidateAll(1)
	notAfter := time.Now().Add(24 * time.Hour)

	for i := range mintedTokenMaxEntries + 10 {
		_, _, err := m.Mint(&Principal{Username: fmt.Sprintf("u%d", i)}, fmt.Sprintf("fp-%d", i), notAfter, 1)
		require.NoError(t, err)

		m.mu.Lock()
		size := len(m.tokens)
		fpSize := len(m.byFP)
		m.mu.Unlock()
		assert.LessOrEqual(t, size, mintedTokenMaxEntries)
		assert.Equal(t, size, fpSize, "tokens and byFP must stay in sync")
	}

	m.mu.Lock()
	finalSize := len(m.tokens)
	m.mu.Unlock()
	assert.Equal(t, mintedTokenMaxEntries, finalSize)
}

func TestMintedTokens_PrunedPredecessorKeepsSuccessorMapping(t *testing.T) {
	m := NewMintedTokens()
	m.InvalidateAll(1)
	p := &Principal{Username: "u"}

	tok1, _, err := m.Mint(p, "fp-1", time.Now().Add(30*time.Second), 1)
	require.NoError(t, err)
	tok2, _, err := m.Mint(p, "fp-1", time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	require.NotEqual(t, tok1, tok2)

	// Force the replaced token past expiry and evict it via Lookup. The
	// fingerprint mapping shared with its successor must survive the
	// eviction.
	m.mu.Lock()
	m.tokens[tok1].expiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()
	_, ok := m.Lookup(tok1)
	require.False(t, ok)

	tok3, _, err := m.Mint(p, "fp-1", time.Now().Add(time.Hour), 1)
	require.NoError(t, err)
	require.Equal(t, tok2, tok3)
}
