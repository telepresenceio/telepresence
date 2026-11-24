package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestHandleInterceptSnapshot_TracksAgentlessIntercepts(t *testing.T) {
	ic := testIntercept("i1", "echo", "app-space", "Deployment")
	ic.Spec.Name = "echo"
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{"i1": ic})
	s.Context = client.WithConfig(s.Context, client.GetDefaultConfig())

	noAgent := []*manager.InterceptInfo{{
		Id:          "i1",
		Disposition: manager.InterceptDispositionType_NO_AGENT,
		Message:     "No agent found for \"echo\"",
		Spec:        ic.Spec,
	}}
	s.handleInterceptSnapshot(newPodAccessTracker(), noAgent)
	require.Contains(t, s.agentless, "i1")

	// A repeated snapshot keeps the entry, so the loss is logged only once.
	s.handleInterceptSnapshot(newPodAccessTracker(), noAgent)
	require.Len(t, s.agentless, 1)
}
