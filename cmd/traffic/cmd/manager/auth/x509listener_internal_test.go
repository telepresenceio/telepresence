package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTest = errors.New("test error")

// TestX509HandshakeLimiter_CapEnforced verifies that TryAcquire refuses once limit slots
// are held, and succeeds again once one is released -- exercised directly rather than
// over real connections, so the assertion isn't timing-flaky.
func TestX509HandshakeLimiter_CapEnforced(t *testing.T) {
	const limit = 16
	l := newX509HandshakeLimiter(limit)

	for i := range limit {
		require.True(t, l.TryAcquire(), "slot %d should be available", i)
	}
	assert.False(t, l.TryAcquire(), "the slot beyond the cap must be refused")

	l.Release()
	assert.True(t, l.TryAcquire(), "a released slot must become available again")
}

// TestRejectLog_WarnRateLimited verifies that reject logs at most one Warn per
// x509RejectWarnInterval regardless of how many rejections occur, and resumes warning
// once the interval has elapsed.
func TestRejectLog_WarnRateLimited(t *testing.T) {
	r := &rejectLog{}
	ctx := context.Background()

	// First rejection always warns (lastWarn is zero).
	r.reject(ctx, "1.2.3.4", errTest)
	r.mu.Lock()
	firstWarn := r.lastWarn
	countAfterFirst := r.count
	r.mu.Unlock()
	assert.False(t, firstWarn.IsZero())
	assert.Equal(t, 0, countAfterFirst)

	// Rejections within the interval accumulate without warning again.
	r.reject(ctx, "1.2.3.4", errTest)
	r.reject(ctx, "1.2.3.4", errTest)
	r.mu.Lock()
	sameWarn := r.lastWarn
	countAfterMore := r.count
	r.mu.Unlock()
	assert.Equal(t, firstWarn, sameWarn)
	assert.Equal(t, 2, countAfterMore)

	// Force the interval to have elapsed, then the next rejection warns again.
	r.mu.Lock()
	r.lastWarn = time.Now().Add(-x509RejectWarnInterval)
	r.mu.Unlock()
	r.reject(ctx, "1.2.3.4", errTest)
	r.mu.Lock()
	newWarn := r.lastWarn
	countAfterElapsed := r.count
	r.mu.Unlock()
	assert.True(t, newWarn.After(sameWarn))
	assert.Equal(t, 0, countAfterElapsed)
}
