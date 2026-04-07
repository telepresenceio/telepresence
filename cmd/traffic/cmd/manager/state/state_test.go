package state

import (
	"context"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
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
	s.ctx = testutil.NewContext(s.T(), false)
	s.state = &State{
		backgroundCtx:    s.ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, 5*time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, 5*time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
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
		a := assert.New(t)

		helloAgent := testAgents["hello"]
		helloProAgent := testAgents["helloPro"]
		demoAgent1 := testAgents["demo1"]
		demoAgent2 := testAgents["demo2"]

		clock := &FakeClock{}
		m := mutator.NewWatcher()
		ctx = mutator.WithMap(ctx, m)
		g := log.NewGroup(ctx)
		st := NewState(ctx, g, nil)

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
		a := assert.New(t)

		clock := &FakeClock{}
		epoch := clock.Now()
		g := log.NewGroup(ctx)
		s := NewState(ctx, g, nil)

		s1 := s.AddClient(testClients["alice"], clock.Now())
		s2 := s.AddClient(testClients["bob"], clock.Now())
		s3 := s.AddClient(testClients["cameron"], clock.Now())

		c1 := s.GetClient(s1)
		c2 := s.GetClient(s2)
		c3 := s.GetClient(s3)
		a.NotNil(c1)
		a.NotNil(c2)
		a.NotNil(c3)
		a.Nil(s.GetClient("asdf"))

		a.Equal(testClients["alice"], c1.ClientInfo)

		clock.When = 10

		a.True(c1.Mark(clock.Now()))
		a.True(c2.Mark(clock.Now()))

		moment := epoch.Add(5 * time.Second)
		s.expireSessions(moment, moment)

		a.NotNil(s.GetClient(s1))
		a.NotNil(s.GetClient(s2))
		a.Nil(s.GetClient(s3))

		clock.When = 20

		a.True(c1.Mark(clock.Now()))
		a.True(c2.Mark(clock.Now()))

		moment = epoch.Add(5 * time.Second)
		s.expireSessions(moment, moment)

		a.NotNil(s.GetClient(s1))
		a.NotNil(s.GetClient(s2))

		s.RemoveSession(ctx, s2)

		a.NotNil(s.GetClient(s1))
		a.Nil(s.GetClient(s2))
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

func TestIsInterceptedBy(t *testing.T) {
	t.Parallel()

	ctx := testutil.NewContext(t, false)
	st := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, 5*time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, 5*time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}

	clientID := tunnel.SessionID("client")

	st.intercepts.Store("http", &Intercept{InterceptInfo: &manager.InterceptInfo{
		Id:          "http",
		Disposition: manager.InterceptDispositionType_ACTIVE,
		PodIp:       "10.0.0.1",
		ClientSession: &manager.SessionInfo{
			SessionId: string(clientID),
		},
		Spec: &manager.InterceptSpec{
			Agent:     "demo",
			Namespace: "default",
			Mechanism: "http",
			Client:    "alice",
		},
	}})

	st.intercepts.Store("tcp", &Intercept{InterceptInfo: &manager.InterceptInfo{
		Id:          "tcp",
		Disposition: manager.InterceptDispositionType_ACTIVE,
		PodIp:       "10.0.0.3",
		ClientSession: &manager.SessionInfo{
			SessionId: string(clientID),
		},
		Spec: &manager.InterceptSpec{
			Agent:     "api",
			Namespace: "default",
			Mechanism: "tcp",
			Client:    "alice",
		},
	}})

	require.True(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.1"), "demo", "default", clientID))
	require.True(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.2"), "demo", "default", clientID))
	require.True(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.3"), "api", "default", clientID))
	require.False(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.4"), "api", "default", clientID))
	require.False(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.2"), "other", "default", clientID))
	require.False(t, st.IsInterceptedBy(netip.MustParseAddr("10.0.0.2"), "demo", "other", clientID))
}

func TestSuiteState(testing *testing.T) {
	suite.Run(testing, new(suiteState))
}
