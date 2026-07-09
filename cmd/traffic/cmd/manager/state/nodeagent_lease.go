package state

import (
	"context"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// leaseKey identifies a single claim on a node-agent Job: one session's hold
// on the node-hosted traffic-agent for one workload, taken by
// EnsureAgent(node_agent=true) and released by ReleaseAgent or by the
// session ending. Intercepts are not tracked here; they already have their
// own lifecycle in s.intercepts and are covered directly by
// nodeAgentWanted.
type leaseKey struct {
	sessionID tunnel.SessionID
	name      string
	namespace string
}

// addLease records that sessionID is using the node-agent for the workload
// identified by name and namespace. It is idempotent.
func (s *State) addLease(sessionID tunnel.SessionID, name, namespace string) {
	s.leases.Store(leaseKey{sessionID: sessionID, name: name, namespace: namespace}, struct{}{})
}

// removeLease drops sessionID's claim on the node-agent for name/namespace,
// if any, and reports whether a lease was actually present.
func (s *State) removeLease(sessionID tunnel.SessionID, name, namespace string) bool {
	_, loaded := s.leases.LoadAndDelete(leaseKey{sessionID: sessionID, name: name, namespace: namespace})
	return loaded
}

// nodeAgentWanted reports whether anything currently claims the node-agent
// for the workload identified by name and namespace: a live (non-REMOVED,
// non-child) node-agent intercept, or a lease whose session is still
// present in s.clients. It is the single predicate shared by the reap
// finalizers, ReleaseAgent, and the periodic reconciler, so that a Job is
// reaped exactly when every path agrees nothing needs it anymore.
func (s *State) nodeAgentWanted(name, namespace string) bool {
	if s.findLiveNodeAgentIntercept(name, namespace, "") != nil {
		return true
	}
	wanted := false
	s.leases.Range(func(k leaseKey, _ struct{}) bool {
		if k.name != name || k.namespace != namespace {
			return true
		}
		if _, ok := s.clients.Load(k.sessionID); ok {
			wanted = true
			return false
		}
		return true
	})
	return wanted
}

// nodeAgentReapFinalizer returns an InterceptFinalizer that reaps the
// node-agent Job for a node-agent intercept's agent and namespace, unless
// nodeAgentWanted reports that something else still claims it (another live
// node-agent intercept, or a lease with a live session -- e.g. a concurrent
// ingest of the same workload). AddIntercept and RestoreIntercepts both
// register this for every node-agent intercept, so the reap decision is
// made the same way regardless of which of the two registered it.
//
// It runs after the intercept it was registered for has already been
// removed from s.intercepts: RemoveIntercept deletes the entry before
// calling terminate (which invokes the finalizers), so nodeAgentWanted never
// sees that intercept as still live.
func (s *State) nodeAgentReapFinalizer() InterceptFinalizer {
	return func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
		agent, ns := interceptInfo.Spec.GetAgent(), interceptInfo.Spec.GetNamespace()
		if s.nodeAgentWanted(agent, ns) {
			return nil
		}
		return reapNodeAgentJobs(ctx, agent, ns)
	}
}

// ReleaseAgent releases sessionID's claim (taken by EnsureAgent when
// node_agent is requested) on the node-agent for the workload identified by
// name and namespace. It is a genuine no-op if sessionID holds no such lease
// (including a claim on a sidecar agent, which never takes one) -- it never
// reaps a Job on behalf of a claim it did not itself hold, leaving that to
// the reap finalizers and the reconciler. When sessionID did hold the
// lease, it is dropped and, once nodeAgentWanted reports that nothing else
// claims the agent, its Job is reaped.
func (s *State) ReleaseAgent(ctx context.Context, sessionID tunnel.SessionID, name, namespace string) error {
	if !s.removeLease(sessionID, name, namespace) {
		return nil
	}
	if s.nodeAgentWanted(name, namespace) {
		return nil
	}
	return reapNodeAgentJobs(ctx, name, namespace)
}
