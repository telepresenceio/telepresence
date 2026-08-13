package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
)

func TestFilterInterceptDeltasRemovesInterceptThatStopsMatching(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	source := make(chan cache.Delta[string, *state.Intercept], 2)
	filtered := filterInterceptDeltas(ctx, source, func(_ string, intercept *state.Intercept) bool {
		return intercept.Spec.Agent == "selected"
	})
	selected := &state.Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:   "client:intercept",
		Spec: &rpc.InterceptSpec{Agent: "selected"},
	}}
	source <- cache.Delta[string, *state.Intercept]{
		Upserts: map[string]*state.Intercept{selected.Id: selected},
	}
	first := <-filtered
	require.Contains(t, first.Upserts, selected.Id)
	require.Empty(t, first.Removals)

	dropped := &state.Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:   selected.Id,
		Spec: &rpc.InterceptSpec{Agent: "dropped"},
	}}
	source <- cache.Delta[string, *state.Intercept]{
		Upserts: map[string]*state.Intercept{dropped.Id: dropped},
	}
	second := <-filtered
	require.Empty(t, second.Upserts)
	require.Contains(t, second.Removals, selected.Id)
	require.Same(t, selected, second.Removals[selected.Id])
}
