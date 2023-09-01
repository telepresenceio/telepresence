package state

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type suiteState struct {
	suite.Suite

	ctx   context.Context
	state *state
}

func (s *suiteState) SetupTest() {
	s.ctx = dlog.NewTestContext(s.T(), false)
	s.state = &state{
		backgroundCtx:   s.ctx,
		sessions:        make(map[string]SessionState),
		agentsByName:    make(map[string]map[string]*manager.AgentInfo),
		cfgMapLocks:     make(map[string]*sync.Mutex),
		interceptStates: make(map[string]*interceptState),
		timedLogLevel:   log.NewTimedLevel("debug", log.SetLevel),
		llSubs:          newLoglevelSubscribers(),
	}
}

type FakeClock struct {
	When int
}

func (fc *FakeClock) Now() time.Time {
	base := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	offset := time.Duration(fc.When) * time.Second
	return base.Add(offset)
}

func (s *suiteState) TestStateInternal() {
	ctx := context.Background()

	testAgents := testdata.GetTestAgents(s.T())
	testClients := testdata.GetTestClients(s.T())

	s.T().Run("agents", func(t *testing.T) {
		a := assertNew(t)

		helloAgent := testAgents["hello"]
		helloProAgent := testAgents["helloPro"]
		demoAgent1 := testAgents["demo1"]
		demoAgent2 := testAgents["demo2"]

		clock := &FakeClock{}
		s := NewState(ctx).(*state)

		h := s.AddAgent(helloAgent, clock.Now())
		hp := s.AddAgent(helloProAgent, clock.Now())
		d1 := s.AddAgent(demoAgent1, clock.Now())
		d2 := s.AddAgent(demoAgent2, clock.Now())

		a.Equal(helloAgent, s.GetAgent(h))
		a.Equal(helloProAgent, s.GetAgent(hp))
		a.Equal(demoAgent1, s.GetAgent(d1))
		a.Equal(demoAgent2, s.GetAgent(d2))

		agents := s.getAllAgents()
		a.Len(agents, 4)
		a.Contains(agents, helloAgent)
		a.Contains(agents, helloProAgent)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = s.getAgentsByName("hello", "default")
		a.Len(agents, 1)
		a.Contains(agents, helloAgent)

		agents = s.getAgentsByName("hello-pro", "default")
		a.Len(agents, 1)
		a.Contains(agents, helloProAgent)

		agents = s.getAgentsByName("demo", "default")
		a.Len(agents, 2)
		a.Contains(agents, demoAgent1)
		a.Contains(agents, demoAgent2)

		agents = s.getAgentsByName("does-not-exist", "default")
		a.Len(agents, 0)

		agents = s.getAgentsByName("hello", "does-not-exist")
		a.Len(agents, 0)
	})

	s.T().Run("presence-redundant", func(t *testing.T) {
		a := assertNew(t)

		clock := &FakeClock{}
		epoch := clock.Now()
		s := NewState(ctx)

		c1 := s.AddClient(testClients["alice"], clock.Now())
		c2 := s.AddClient(testClients["bob"], clock.Now())
		c3 := s.AddClient(testClients["cameron"], clock.Now())

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.NotNil(s.GetClient(c3))
		a.Nil(s.GetClient("asdf"))

		a.Equal(testClients["alice"], s.GetClient(c1))

		clock.When = 10

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c1}}, clock.Now()))
		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c2}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: "asdf"}}, clock.Now()))

		moment := epoch.Add(5 * time.Second)
		s.ExpireSessions(ctx, moment, moment)

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		clock.When = 20

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c1}}, clock.Now()))
		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c2}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c3}}, clock.Now()))

		moment = epoch.Add(5 * time.Second)
		s.ExpireSessions(ctx, moment, moment)

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		s.RemoveSession(ctx, c2)

		a.NotNil(s.GetClient(c1))
		a.Nil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c1}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c2}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: c3}}, clock.Now()))
	})
}

func (s *suiteState) TestAddClient() {
	// given
	now := time.Now()

	// when
	s.state.AddClient(&manager.ClientInfo{
		Name:      "my-client",
		InstallId: "1234",
		Product:   "5668",
		Version:   "2.14.2",
		ApiKey:    "xxxx",
	}, now)

	// then
	assert.Len(s.T(), s.state.sessions, 1)
}

func (s *suiteState) TestRemoveSession() {
	// given
	now := time.Now()
	s.state.sessions["session-1"] = newClientSessionState(s.ctx, now)
	s.state.sessions["session-2"] = newAgentSessionState(s.ctx, now)

	// when
	s.state.RemoveSession(s.ctx, "session-1")
	s.state.RemoveSession(s.ctx, "session-2") // won't fail trying to delete consumption.

	// then
	assert.Len(s.T(), s.state.sessions, 0)
}

func TestSuiteState(testing *testing.T) {
	suite.Run(testing, new(suiteState))
}
