package mutator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func TestAgentConfigUpdatesDoNotBlockUnrelatedWorkloads(t *testing.T) {
	cw := NewWatcher().(*configWatcher)
	namespace := "app-space"
	cw.Store(&agentconfig.Sidecar{AgentName: "blocked", Namespace: namespace})
	want := &agentconfig.Sidecar{AgentName: "ready", Namespace: namespace}
	cw.Store(want)

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := cw.Update("blocked", namespace, func(sc *agentconfig.Sidecar) (*agentconfig.Sidecar, error) {
			close(entered)
			<-release
			return sc, nil
		})
		errCh <- err
	}()

	<-entered
	got := make(chan *agentconfig.Sidecar, 1)
	go func() {
		got <- cw.Get("ready", namespace)
	}()

	select {
	case sc := <-got:
		require.Equal(t, want, sc)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("unrelated workload config read blocked behind another workload update")
	}

	close(release)
	<-done
	require.NoError(t, <-errCh)
}
