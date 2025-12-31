package state

import (
	"context"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type suiteState struct {
	suite.Suite

	ctx   context.Context
	state *State
}

func (s *suiteState) SetupTest() {
	s.ctx = dlog.NewTestContext(s.T(), false)
	s.state = &State{
		backgroundCtx:    s.ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, 5*time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, 5*time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel("debug", log.SetLevel),
		llSubs:           newLoglevelSubscribers(),
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
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{})

	testAgents := testdata.GetTestAgents(s.T())
	testClients := testdata.GetTestClients(s.T())

	s.T().Run("agents", func(t *testing.T) {
		a := assertNew(t)

		helloAgent := testAgents["hello"]
		helloProAgent := testAgents["helloPro"]
		demoAgent1 := testAgents["demo1"]
		demoAgent2 := testAgents["demo2"]

		clock := &FakeClock{}
		m := mutator.NewWatcher()
		ctx = mutator.WithMap(ctx, m)
		g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
		st := NewState(ctx, g)

		h, err := st.AddAgent(ctx, helloAgent, clock.Now())
		require.NoError(t, err)
		hp, err := st.AddAgent(ctx, helloProAgent, clock.Now())
		require.NoError(t, err)
		d1, err := st.AddAgent(ctx, demoAgent1, clock.Now())
		require.NoError(t, err)
		d2, err := st.AddAgent(ctx, demoAgent2, clock.Now())
		require.NoError(t, err)

		a.Equal(helloAgent, st.GetAgent(h).AgentInfo)
		a.Equal(helloProAgent, st.GetAgent(hp).AgentInfo)
		a.Equal(demoAgent1, st.GetAgent(d1).AgentInfo)
		a.Equal(demoAgent2, st.GetAgent(d2).AgentInfo)
	})

	s.T().Run("presence-redundant", func(t *testing.T) {
		a := assertNew(t)

		clock := &FakeClock{}
		epoch := clock.Now()
		g := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
		s := NewState(ctx, g)

		c1 := s.AddClient(testClients["alice"], clock.Now())
		c2 := s.AddClient(testClients["bob"], clock.Now())
		c3 := s.AddClient(testClients["cameron"], clock.Now())

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.NotNil(s.GetClient(c3))
		a.Nil(s.GetClient("asdf"))

		a.Equal(testClients["alice"], s.GetClient(c1).ClientInfo)

		clock.When = 10

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c1)}}, clock.Now()))
		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c2)}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: "asdf"}}, clock.Now()))

		moment := epoch.Add(5 * time.Second)
		s.expireSessions(ctx, moment, moment)

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		clock.When = 20

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c1)}}, clock.Now()))
		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c2)}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c3)}}, clock.Now()))

		moment = epoch.Add(5 * time.Second)
		s.expireSessions(ctx, moment, moment)

		a.NotNil(s.GetClient(c1))
		a.NotNil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		s.RemoveSession(ctx, c2)

		a.NotNil(s.GetClient(c1))
		a.Nil(s.GetClient(c2))
		a.Nil(s.GetClient(c3))

		a.True(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c1)}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c2)}}, clock.Now()))
		a.False(s.MarkSession(&manager.RemainRequest{Session: &manager.SessionInfo{SessionId: string(c3)}}, clock.Now()))
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
	}, now)

	// then
	assert.Equal(s.T(), 1, s.state.clients.Size())
}

func (s *suiteState) TestRemoveSession() {
	// given
	now := time.Now()
	s1 := s.state.AddClient(&manager.ClientInfo{
		Name:      "my-client",
		InstallId: "1234",
		Product:   "5668",
		Version:   "2.14.2",
	}, now)
	s2 := s.state.AddClient(&manager.ClientInfo{
		Name:      "your-client",
		InstallId: "5678",
		Product:   "5668",
		Version:   "2.14.2",
	}, now)

	assert.Equal(s.T(), s.state.CountSessions(), 2)

	s.state.RemoveSession(s.ctx, s1)
	s.state.RemoveSession(s.ctx, s2) // won't fail trying to delete consumption.

	assert.Equal(s.T(), s.state.CountSessions(), 0)
}

func TestSuiteState(testing *testing.T) {
	suite.Run(testing, new(suiteState))
}
