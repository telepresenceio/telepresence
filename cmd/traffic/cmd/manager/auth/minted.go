package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// mintedTokenTTL is the maximum lifetime of a token minted by the x509 auth listener,
// further clamped to the certificate's own validity by Mint's notAfter argument.
const mintedTokenTTL = time.Hour

// mintedTokenBytes is the amount of randomness backing a minted token, before
// base64 encoding.
const mintedTokenBytes = 32

// mintedTokenReuseMargin is the minimum remaining validity an existing token must have
// for Mint to return it instead of minting a fresh one. It must exceed the client's own
// refresh margin (one minute): a client that refreshes near expiry would otherwise be
// handed the same near-expired token back, and would repeat a full TLS handshake on
// every call until the token actually expires. A replaced token stays valid until its
// own expiry, so calls already carrying it keep working.
const mintedTokenReuseMargin = 2 * time.Minute

// mintedTokenMaxEntries bounds the store so a client that repeatedly presents distinct
// certificates cannot grow it without limit. The entry with the soonest expiry is
// evicted to make room once the cap is reached.
const mintedTokenMaxEntries = 1024

// ErrCAGenerationMismatch is returned by Mint when the caGeneration a certificate was
// verified against no longer matches the store's current CA generation: the client CA
// pool was swapped, invalidating every previously minted token, between verification and
// minting. The caller should re-verify against the current pool and retry Mint.
var ErrCAGenerationMismatch = errors.New("client CA generation mismatch")

type mintedTokenEntry struct {
	token       string
	principal   *Principal
	fingerprint string
	expiresAt   time.Time
}

// MintedTokens is an in-memory store of opaque bearer tokens minted by the x509 auth
// listener for callers that authenticated with a client certificate rather than a
// Kubernetes bearer token. Tokens do not survive a manager restart.
type MintedTokens struct {
	mu           sync.Mutex
	tokens       map[string]*mintedTokenEntry // by token
	byFP         map[string]*mintedTokenEntry // by certificate fingerprint
	caGeneration uint64                       // 0 means no CA has been loaded yet
}

// NewMintedTokens creates an empty token store.
func NewMintedTokens() *MintedTokens {
	return &MintedTokens{
		tokens: make(map[string]*mintedTokenEntry),
		byFP:   make(map[string]*mintedTokenEntry),
	}
}

// Mint returns a token bound to p and certFingerprint, valid until the earlier of
// mintedTokenTTL from now and notAfter. It refuses with ErrCAGenerationMismatch
// unless caGeneration matches the store's current CA generation (zero never does).
// An existing token for certFingerprint that stays valid beyond
// mintedTokenReuseMargin is returned unchanged instead of minting a new one.
func (m *MintedTokens) Mint(p *Principal, certFingerprint string, notAfter time.Time, caGeneration uint64) (token string, expiresAt time.Time, err error) {
	now := time.Now()
	expiresAt = now.Add(mintedTokenTTL)
	if notAfter.Before(expiresAt) {
		expiresAt = notAfter
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if caGeneration == 0 || caGeneration != m.caGeneration {
		return "", time.Time{}, ErrCAGenerationMismatch
	}

	m.pruneExpiredLocked(now)

	if e, ok := m.byFP[certFingerprint]; ok && now.Add(mintedTokenReuseMargin).Before(e.expiresAt) {
		return e.token, e.expiresAt, nil
	}

	if len(m.tokens) >= mintedTokenMaxEntries {
		m.evictSoonestLocked()
	}

	buf := make([]byte, mintedTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)

	e := &mintedTokenEntry{token: token, principal: p, fingerprint: certFingerprint, expiresAt: expiresAt}
	m.tokens[token] = e
	m.byFP[certFingerprint] = e
	return token, expiresAt, nil
}

// Lookup returns the Principal bound to token and whether it was found and has not
// expired. An expired entry is evicted and never matches.
func (m *MintedTokens) Lookup(token string) (*Principal, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tokens[token]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(e.expiresAt) {
		m.deleteLocked(e)
		return nil, false
	}
	return e.principal, true
}

// InvalidateAll discards every minted token and records newGeneration as the current
// CA generation. Monotonic: a newGeneration that is not strictly greater than the
// current one is ignored.
func (m *MintedTokens) InvalidateAll(newGeneration uint64) {
	m.mu.Lock()
	if newGeneration > m.caGeneration {
		m.tokens = make(map[string]*mintedTokenEntry)
		m.byFP = make(map[string]*mintedTokenEntry)
		m.caGeneration = newGeneration
	}
	m.mu.Unlock()
}

func (m *MintedTokens) pruneExpiredLocked(now time.Time) {
	for _, e := range m.tokens {
		if !now.Before(e.expiresAt) {
			m.deleteLocked(e)
		}
	}
}

// evictSoonestLocked removes the entry with the soonest expiry, to make room in a full
// store for a new one.
func (m *MintedTokens) evictSoonestLocked() {
	var soonest *mintedTokenEntry
	for _, e := range m.tokens {
		if soonest == nil || e.expiresAt.Before(soonest.expiresAt) {
			soonest = e
		}
	}
	if soonest != nil {
		m.deleteLocked(soonest)
	}
}

func (m *MintedTokens) deleteLocked(e *mintedTokenEntry) {
	delete(m.tokens, e.token)
	// A replaced near-expiry token shares its fingerprint with its successor; only
	// remove the fingerprint mapping when it still refers to the entry being deleted.
	if cur, ok := m.byFP[e.fingerprint]; ok && cur == e {
		delete(m.byFP, e.fingerprint)
	}
}
