package state_test

import (
	"context"
	"testing"
	"time"

	manager "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/state"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/test"
)

type FakeClock struct {
	When int
}

func (fc *FakeClock) Now() time.Time {
	base := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	offset := time.Duration(fc.When) * time.Second
	return base.Add(offset)
}

func TestStateInternal(topT *testing.T) {
	ctx := context.Background()

	testAgents := testdata.GetTestAgents(topT)
	testClients := testdata.GetTestClients(topT)

	topT.Run("agents", func(t *testing.T) {
		a := assertNew(t)

		helloAgent := testAgents["hello"]
		helloProAgent := testAgents["helloPro"]
		demoAgent1 := testAgents["demo1"]
		demoAgent2 := testAgents["demo2"]

		clock := &FakeClock{}
		state := manager.NewState(ctx)

		h := state.AddAgent(helloAgent, clock.Now())
		hp := state.AddAgent(helloProAgent, clock.Now())
		d1 := state.AddAgent(demoAgent1, clock.Now())
		d2 := state.AddAgent(demoAgent2, clock.Now())

		a.Equal(helloAgent, state.GetAgent(h))
		a.Equal(helloProAgent, state.GetAgent(hp))
		a.Equal(demoAgent1, state.GetAgent(d1))
		a.Equal(demoAgent2, state.GetAgent(d2))

		agents := state.GetAllAgents()
		a.Len(agents, 4)
		a.Contains(agents, helloAgent)
		a.Contains(agents, helloProAgent)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = state.GetAgentsByName("hello", "default")
		a.Len(agents, 1)
		a.Contains(agents, helloAgent)

		agents = state.GetAgentsByName("hello-pro", "default")
		a.Len(agents, 1)
		a.Contains(agents, helloProAgent)

		agents = state.GetAgentsByName("demo", "default")
		a.Len(agents, 2)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = state.GetAgentsByName("does-not-exist", "default")
		a.Len(agents, 0)
	})

	topT.Run("presence-redundant", func(t *testing.T) {
		a := assertNew(t)

		clock := &FakeClock{}
		epoch := clock.Now()
		state := manager.NewState(ctx)

		c1 := state.AddClient(testClients["alice"], clock.Now())
		c2 := state.AddClient(testClients["bob"], clock.Now())
		c3 := state.AddClient(testClients["cameron"], clock.Now())

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.True(state.HasClient(c3))
		a.False(state.HasClient("asdf"))

		a.Equal(testClients["alice"], state.GetClient(c1))

		clock.When = 10

		a.True(state.Mark(c1, clock.Now()))
		a.True(state.Mark(c2, clock.Now()))
		a.False(state.Mark("asdf", clock.Now()))

		state.ExpireSessions(epoch.Add(5 * time.Second))

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.False(state.HasClient(c3))

		clock.When = 20

		a.True(state.Mark(c1, clock.Now()))
		a.True(state.Mark(c2, clock.Now()))
		a.False(state.Mark(c3, clock.Now()))

		state.ExpireSessions(epoch.Add(5 * time.Second))

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.False(state.HasClient(c3))

		state.RemoveSession(c2)

		a.True(state.HasClient(c1))
		a.False(state.HasClient(c2))
		a.False(state.HasClient(c3))

		a.True(state.Mark(c1, clock.Now()))
		a.False(state.Mark(c2, clock.Now()))
		a.False(state.Mark(c3, clock.Now()))
	})
}
