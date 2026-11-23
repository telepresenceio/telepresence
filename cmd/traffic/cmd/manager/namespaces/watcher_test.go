package namespaces

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cluster test cannot show this: with nothing managed, no namespace has a started informer, so
// the agent-injector answers without a patch whether the manager reads the scope as nil or empty.
func TestEmptySelectionIsNotClusterScope(t *testing.T) {
	h := &namesspacesHandle{interestedParties: make(map[uuid.UUID]chan<- struct{})}
	ctx := context.WithValue(context.Background(), nsKey{}, h)

	h.set([]string{})

	got := Get(ctx)
	require.NotNil(t, got, "an empty selection must not read as cluster-wide scope")
	assert.Empty(t, got)
	assert.False(t, Manages(ctx, "any-namespace"))
}
