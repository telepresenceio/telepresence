package manager_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/telepresence2/pkg/manager"
	"github.com/datawire/telepresence2/pkg/rpc"
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

	testAgents := manager.GetTestAgents(topT)
	testClients := manager.GetTestClients(topT)

	topT.Run("agents", func(t *testing.T) {
		a := assert.New(t)

		helloAgent := testAgents["hello"]
		helloProAgent := testAgents["helloPro"]
		demoAgent1 := testAgents["demo1"]
		demoAgent2 := testAgents["demo2"]

		clock := &FakeClock{}
		state := manager.NewState(ctx, clock)

		h := state.AddAgent(helloAgent)
		hp := state.AddAgent(helloProAgent)
		d1 := state.AddAgent(demoAgent1)
		d2 := state.AddAgent(demoAgent2)

		a.Equal(helloAgent, state.GetAgent(h))
		a.Equal(helloProAgent, state.GetAgent(hp))
		a.Equal(demoAgent1, state.GetAgent(d1))
		a.Equal(demoAgent2, state.GetAgent(d2))

		var agents []*rpc.AgentInfo

		agents = state.GetAgents()
		a.Len(agents, 4)
		a.Contains(agents, helloAgent)
		a.Contains(agents, helloProAgent)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = state.GetAgentsByName("hello")
		a.Len(agents, 1)
		a.Contains(agents, helloAgent)

		agents = state.GetAgentsByName("hello-pro")
		a.Len(agents, 1)
		a.Contains(agents, helloProAgent)

		agents = state.GetAgentsByName("demo")
		a.Len(agents, 2)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = state.GetAgentsByName("does-not-exist")
		a.Len(agents, 0)
	})

	topT.Run("presence-redundant", func(t *testing.T) {
		a := assert.New(t)

		clock := &FakeClock{}
		state := manager.NewState(ctx, clock)

		c1 := state.AddClient(testClients["alice"])
		c2 := state.AddClient(testClients["bob"])
		c3 := state.AddClient(testClients["cameron"])

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.True(state.HasClient(c3))
		a.False(state.HasClient("asdf"))

		a.Equal(testClients["alice"], state.GetClient(c1))

		clock.When = 10

		a.True(state.Mark(c1))
		a.True(state.Mark(c2))
		a.False(state.Mark("asdf"))

		state.Expire(clock.Now())

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.False(state.HasClient(c3))

		clock.When = 20

		a.True(state.Mark(c1))
		a.True(state.Mark(c2))
		a.False(state.Mark(c3))

		state.Expire(clock.Now())

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.False(state.HasClient(c3))

		state.Remove(c2)

		a.True(state.HasClient(c1))
		a.False(state.HasClient(c2))
		a.False(state.HasClient(c3))

		a.True(state.Mark(c1))
		a.False(state.Mark(c2))
		a.False(state.Mark(c3))
	})
}
