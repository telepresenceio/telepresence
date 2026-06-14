package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestShortcutEligible(t *testing.T) {
	plain := &manager.InterceptSpec{Mechanism: "tcp"}
	filtered := &manager.InterceptSpec{Mechanism: "http", HeaderFilters: map[string]string{"x-route-key": "me"}}
	pathFiltered := &manager.InterceptSpec{Mechanism: "http", PathFilters: []string{"/api"}}
	wiretap := &manager.InterceptSpec{Mechanism: "tcp", Wiretap: true}

	// All intercepts qualify when the shortcut is global.
	assert.True(t, shortcutEligible(plain, true))
	assert.True(t, shortcutEligible(filtered, true))
	assert.True(t, shortcutEligible(pathFiltered, true))

	// Only unfiltered intercepts qualify when the shortcut is not global.
	assert.True(t, shortcutEligible(plain, false))
	assert.False(t, shortcutEligible(filtered, false))
	assert.False(t, shortcutEligible(pathFiltered, false))

	// A wiretap mirrors traffic rather than redirecting it, so it never qualifies.
	assert.False(t, shortcutEligible(wiretap, true))
	assert.False(t, shortcutEligible(wiretap, false))
}
