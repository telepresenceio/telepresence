package manager

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/datawire/telepresence2/pkg/rpc"
	"github.com/stretchr/testify/assert"
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

	topT.Run("counter", func(t *testing.T) {
		a := assert.New(t)

		state := NewState(ctx, wall{})
		i := state.next()
		a.Equal(i+1, state.next())
		a.Equal(i+2, state.next())
		a.Equal(i+3, state.next())
	})

	tcpMech := &rpc.AgentInfo_Mechanism{
		Name:    "tcp",
		Product: "oss",
		Version: "1",
	}
	httpMech := &rpc.AgentInfo_Mechanism{
		Name:    "http",
		Product: "plus",
		Version: "1",
	}
	httpMech2 := &rpc.AgentInfo_Mechanism{
		Name:    "http",
		Product: "plus",
		Version: "2",
	}
	grpcMech := &rpc.AgentInfo_Mechanism{
		Name:    "grpc",
		Product: "plus",
		Version: "1",
	}

	helloAgent := &rpc.AgentInfo{
		Name:       "hello",
		Hostname:   "hello-abcdef-123",
		Product:    "oss",
		Version:    "1",
		Mechanisms: []*rpc.AgentInfo_Mechanism{tcpMech},
	}

	helloProAgent := &rpc.AgentInfo{
		Name:       "hello-pro",
		Hostname:   "hello-pro-abcdef-123",
		Product:    "plus",
		Version:    "1",
		Mechanisms: []*rpc.AgentInfo_Mechanism{tcpMech, httpMech, grpcMech},
	}

	demoAgent1 := &rpc.AgentInfo{
		Name:       "demo",
		Hostname:   "demo-abcdef-123",
		Product:    "plus",
		Version:    "1",
		Mechanisms: []*rpc.AgentInfo_Mechanism{tcpMech, httpMech, grpcMech},
	}

	demoAgent2 := &rpc.AgentInfo{
		Name:       "demo",
		Hostname:   "demo-abcdef-456",
		Product:    "plus",
		Version:    "1",
		Mechanisms: []*rpc.AgentInfo_Mechanism{grpcMech, httpMech, tcpMech},
	}

	topT.Run("mechanisms", func(t *testing.T) {
		a := assert.New(t)

		empty := []*rpc.AgentInfo_Mechanism{}
		oss := helloAgent.Mechanisms
		plus := helloProAgent.Mechanisms
		sameAsPlus := []*rpc.AgentInfo_Mechanism{httpMech, grpcMech, tcpMech}
		plus2 := []*rpc.AgentInfo_Mechanism{tcpMech, grpcMech, httpMech2}
		bogus := []*rpc.AgentInfo_Mechanism{tcpMech, httpMech, httpMech2}

		a.False(mechanismsAreTheSame(empty, empty))
		a.False(mechanismsAreTheSame(oss, plus))
		a.False(mechanismsAreTheSame(plus, plus2))
		a.False(mechanismsAreTheSame(plus, bogus))
		a.True(mechanismsAreTheSame(plus, sameAsPlus))
		a.True(mechanismsAreTheSame(demoAgent1.Mechanisms, demoAgent2.Mechanisms))
	})

	topT.Run("agents", func(t *testing.T) {
		a := assert.New(t)

		a.True(agentsAreCompatible([]*rpc.AgentInfo{demoAgent1, demoAgent2}))
		a.True(agentsAreCompatible([]*rpc.AgentInfo{helloAgent}))
		a.True(agentsAreCompatible([]*rpc.AgentInfo{helloProAgent}))
		a.False(agentsAreCompatible([]*rpc.AgentInfo{}))
		a.False(agentsAreCompatible([]*rpc.AgentInfo{helloAgent, helloProAgent}))

		clock := &FakeClock{}
		state := NewState(ctx, clock)

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

	makeClient := func(name string) *rpc.ClientInfo {
		return &rpc.ClientInfo{
			Name:      name,
			InstallId: fmt.Sprintf("install-id-for-%s", name),
			Product:   "telepresence",
			Version:   "1",
		}
	}

	topT.Run("presence-redundant", func(t *testing.T) {
		a := assert.New(t)

		clock := &FakeClock{}
		state := NewState(ctx, clock)

		client1 := makeClient("client1")
		c1 := state.AddClient(client1)
		c2 := state.AddClient(makeClient("client2"))
		c3 := state.AddClient(makeClient("client3"))

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.True(state.HasClient(c3))
		a.False(state.HasClient("asdf"))

		a.Equal(client1, state.GetClient(c1))

		clock.When = 10

		a.True(state.Mark(c1))
		a.True(state.Mark(c2))
		a.False(state.Mark("asdf"))

		state.Expire(clock.Now())

		a.True(state.HasClient(c1))
		a.True(state.HasClient(c2))
		a.False(state.HasClient(c3))

		a.Len(state.sessions.entries, 2) // XXX

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
