package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// This file covers the read authorization of WatchAgents and WatchAgentsDelta.

func agentInfoNames(agents map[string]*rpc.AgentInfo, compact bool, t *testing.T, into map[string]struct{}) {
	for _, a := range agents {
		require.Equal(t, compact, a.ContainerEnvironmentOmitted)
		into[a.Namespace+"/"+a.Name] = struct{}{}
	}
}

// setCompact sets whether the session's client asks for the compact form.
func (e *readEnv) setCompact(compact bool) {
	cs := e.mgr.State().GetClient(tunnel.SessionID(e.sess.SessionId))
	cs.SupportsCompactAgentInfo = compact
}

type agentWatch struct {
	name  string
	watch func(e *readEnv, compact bool) (<-chan error, func(), func() []string)
}

var agentWatches = []agentWatch{
	{"WatchAgents", func(e *readEnv, compact bool) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.AgentInfoSnapshot]) error {
			return e.mgr.WatchAgents(e.sess, s)
		}, func(m *rpc.AgentInfoSnapshot, into map[string]struct{}) {
			clear(into)
			for _, a := range m.Agents {
				require.Equal(e.t, compact, a.ContainerEnvironmentOmitted)
				into[a.Namespace+"/"+a.Name] = struct{}{}
			}
		})
	}},
	{"WatchAgentsDelta", func(e *readEnv, compact bool) (<-chan error, func(), func() []string) {
		return startWatch(e, func(s *fakeServerStream[rpc.AgentInfoDelta]) error {
			return e.mgr.WatchAgentsDelta(e.sess, s)
		}, func(m *rpc.AgentInfoDelta, into map[string]struct{}) {
			agentInfoNames(m.Upserts, compact, e.t, into)
		})
	}},
}

func TestReadAccess_AgentWatches(t *testing.T) {
	for _, w := range agentWatches {
		for _, compact := range []bool{false, true} {
			for _, c := range readCallers {
				name := w.name + "/" + c.name
				if compact {
					name += "/compact"
				}
				t.Run(name, func(t *testing.T) {
					e := newReadEnv(t, auth.ModeEnforcing, c.allow)
					e.setCompact(compact)
					e.restoreAgents()
					errCh, stop, names := w.watch(e, compact)
					require.Equal(t, append([]string(nil), c.visible...), append([]string(nil), names()...))
					stop()
					require.NoError(t, waitErr(t, errCh))
				})
			}
		}
	}
}

// TestReadAccess_AgentWatches_WorkloadUnavailable: a workload review that
// cannot be performed ends the watch with Unavailable.
func TestReadAccess_AgentWatches_WorkloadUnavailable(t *testing.T) {
	grant := readCallers[2]
	for _, w := range agentWatches {
		t.Run(w.name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, grant.allow)
			e.restoreAgents()
			failing := failReviews(e, isWorkloadReview)
			errCh, stop, _ := w.watch(e, false)
			require.Equal(t, codes.Unavailable, status.Code(waitErr(t, errCh)))
			stop()

			failing.Store(false)
			errCh, stop, names := w.watch(e, false)
			require.Equal(t, []string{"default/hello"}, names())
			stop()
			require.NoError(t, waitErr(t, errCh))
		})
	}
}

// TestReadAccess_AgentWatches_NamespaceUnavailable: a namespace review that
// cannot be performed fails the call with Unavailable.
func TestReadAccess_AgentWatches_NamespaceUnavailable(t *testing.T) {
	for _, w := range agentWatches {
		t.Run(w.name, func(t *testing.T) {
			e := newReadEnv(t, auth.ModeEnforcing, readCallers[1].allow)
			e.restoreAgents()
			failing := failReviews(e, isNamespaceReview)
			errCh, stop, _ := w.watch(e, false)
			require.Equal(t, codes.Unavailable, status.Code(waitErr(t, errCh)))
			stop()

			failing.Store(false)
			errCh, stop, names := w.watch(e, false)
			require.Equal(t, []string{"default/demo", "default/hello"}, names())
			stop()
			require.NoError(t, waitErr(t, errCh))
		})
	}
}

// TestReadAccess_AgentWatches_NotEnforcing: outside enforcing mode every agent
// of the namespace is sent and no workload is reviewed.
func TestReadAccess_AgentWatches_NotEnforcing(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModePermissive, auth.ModeDisabled} {
		for _, w := range agentWatches {
			t.Run(string(mode)+"/"+w.name, func(t *testing.T) {
				e := newReadEnv(t, mode, isConnectReview)
				e.restoreAgents()
				errCh, stop, names := w.watch(e, false)
				require.Equal(t, []string{"default/demo", "default/hello"}, names())
				stop()
				require.NoError(t, waitErr(t, errCh))
				require.Zero(t, e.attachmentReviews(true))
			})
		}
	}
}

// TestReadAccess_AgentWatchDelta_Removals: a removal is sent only for an agent
// that was sent.
func TestReadAccess_AgentWatchDelta_Removals(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, readCallers[2].allow)
	e.restoreAgents()
	stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentInfoDelta]) error {
		return e.mgr.WatchAgentsDelta(e.sess, s)
	})
	initial := collect(stream.ch)
	require.NotEmpty(t, initial)

	st := e.mgr.State()
	byName := map[string]tunnel.SessionID{}
	for id, a := range st.LoadMatchingAgents(func(tunnel.SessionID, *state.AgentSession) bool { return true }) {
		byName[a.Namespace+"/"+a.Name] = id
	}
	// demo was never sent to the caller; hello was.
	sessionIDs := []tunnel.SessionID{byName["default/demo"], byName["default/hello"]}
	require.NotEmpty(t, sessionIDs[0])
	require.NotEmpty(t, sessionIDs[1])
	for _, id := range sessionIDs {
		st.RemoveSession(e.sctx, id)
	}
	var removals []string
	for _, m := range collect(stream.ch) {
		removals = append(removals, m.Removals...)
	}
	require.Equal(t, []string{string(sessionIDs[1])}, removals)
	require.NoError(t, stop())
}

// TestReadAccess_AgentWatchDelta_FirstMessage: the first message is sent even
// when filtering leaves it empty; later messages that filtering leaves empty
// are not sent.
func TestReadAccess_AgentWatchDelta_FirstMessage(t *testing.T) {
	e := newReadEnv(t, auth.ModeEnforcing, readCallers[0].allow)
	e.restoreAgents()
	stream, stop := runWatch(e.pctx, func(s *fakeServerStream[rpc.AgentInfoDelta]) error {
		return e.mgr.WatchAgentsDelta(e.sess, s)
	})
	first := collect(stream.ch)
	require.Len(t, first, 1)
	require.Empty(t, first[0].Upserts)
	require.Empty(t, first[0].Removals)

	st := e.mgr.State()
	for id, a := range st.LoadMatchingAgents(func(tunnel.SessionID, *state.AgentSession) bool { return true }) {
		if a.Namespace == "default" && a.Name == "demo" {
			st.RemoveSession(e.sctx, id)
		}
	}
	require.Empty(t, collect(stream.ch))
	require.NoError(t, stop())
}
