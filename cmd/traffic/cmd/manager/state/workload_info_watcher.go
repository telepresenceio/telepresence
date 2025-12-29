package state

import (
	"context"
	"math"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type WorkloadInfoWatcher interface {
	Watch(context.Context, rpc.Manager_WatchWorkloadsServer) error
}

type workloadInfoWatcher struct {
	*State
	clientSession  tunnel.SessionID
	namespace      string
	stream         rpc.Manager_WatchWorkloadsServer
	workloadEvents *xsync.Map[string, *rpc.WorkloadEvent]
	lastEvents     map[string]*rpc.WorkloadEvent
	start          time.Time
	sendTimer      *time.Timer
}

func (s *State) NewWorkloadInfoWatcher(clientSession tunnel.SessionID, namespace string) WorkloadInfoWatcher {
	return &workloadInfoWatcher{
		State:         s,
		clientSession: clientSession,
		namespace:     namespace,
	}
}

func (wf *workloadInfoWatcher) Watch(ctx context.Context, stream rpc.Manager_WatchWorkloadsServer) error {
	wf.start = time.Now()
	defer func() {
		wf.sendTimer.Stop()
		wf.stream = nil
		wf.lastEvents = nil
		wf.workloadEvents = nil
	}()

	wf.sendTimer = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		wf.sendEvents(ctx, false)
	})

	wf.stream = stream
	wf.workloadEvents = xsync.NewMap[string, *rpc.WorkloadEvent]()

	sessionDone, err := wf.SessionDone(wf.clientSession)
	if err != nil {
		return err
	}

	workloadsCh, err := wf.WatchWorkloads(ctx, wf.namespace)
	if err != nil {
		return err
	}

	agentsCh := wf.WatchAgents(ctx, func(_ tunnel.SessionID, info *AgentSession) bool {
		return info.Namespace == wf.namespace
	})

	interceptsCh := wf.WatchIntercepts(ctx, func(_ string, info *Intercept) bool {
		return info.Spec.Namespace == wf.namespace
	})

	// Everything in this loop happens in sequence, even the firing of the timer. This means
	// that there's no concurrency and no need for mutexes.
	initial := true
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case wes, ok := <-workloadsCh:
			if !ok {
				dlog.Debug(ctx, "Workloads channel closed")
				return nil
			}
			wf.handleWorkloadEvents(ctx, wes, initial)
			initial = false
		case agentDelta := <-agentsCh:
			wf.handleAgentDelta(ctx, agentDelta)
		case interceptsDelta := <-interceptsCh:
			wf.handleInterceptDelta(ctx, interceptsDelta)
		}
	}
}

func (wf *workloadInfoWatcher) getIntercepts(name, namespace string) (iis []*rpc.WorkloadInfo_Intercept) {
	wf.intercepts.Range(func(key string, ii *Intercept) bool {
		if name == ii.Spec.Agent && namespace == ii.Spec.Namespace {
			switch ii.Disposition {
			case rpc.InterceptDispositionType_ACTIVE, rpc.InterceptDispositionType_WAITING, rpc.InterceptDispositionType_NO_AGENT:
				iis = append(iis, &rpc.WorkloadInfo_Intercept{
					Client: ii.Spec.Client,
				})
			}
		}
		return true
	})
	return iis
}

func (wf *workloadInfoWatcher) sendEvents(ctx context.Context, sendEmpty bool) {
	// Time to send what we have
	evz := wf.workloadEvents.Size()
	evs := make([]*rpc.WorkloadEvent, 0, evz)
	evm := make(map[string]*rpc.WorkloadEvent, evz)
	wf.workloadEvents.Range(func(k string, rew *rpc.WorkloadEvent) bool {
		evm[k] = rew
		if lew, ok := wf.lastEvents[k]; ok {
			if proto.Equal(lew, rew) {
				return true
			}
		}
		evs = append(evs, rew)
		return true
	})
	wf.workloadEvents.Clear()
	wf.lastEvents = evm

	if !sendEmpty && len(evs) == 0 {
		return
	}
	dlog.Debugf(ctx, "Sending %d WorkloadEvents", len(evs))
	err := wf.stream.Send(&rpc.WorkloadEventsDelta{
		Since:  timestamppb.New(wf.start),
		Events: evs,
	})
	if err != nil {
		dlog.Warnf(ctx, "failed to send workload events delta: %v", err)
		return
	}
	wf.start = time.Now()
}

func (wf *workloadInfoWatcher) resetTicker() {
	wf.sendTimer.Reset(5 * time.Millisecond)
}

func rpcWorkloadState(s workload.State) (state rpc.WorkloadInfo_State) {
	switch s {
	case workload.StateFailure:
		state = rpc.WorkloadInfo_FAILURE
	case workload.StateAvailable:
		state = rpc.WorkloadInfo_AVAILABLE
	case workload.StateProgressing:
		state = rpc.WorkloadInfo_PROGRESSING
	default:
		state = rpc.WorkloadInfo_UNKNOWN_UNSPECIFIED
	}
	return state
}

func rpcWorkload(wl k8sapi.Workload, as rpc.WorkloadInfo_AgentState, iClients []*rpc.WorkloadInfo_Intercept) *rpc.WorkloadInfo {
	return &rpc.WorkloadInfo{
		Kind:             workload.RpcKind(wl.GetKind()),
		Name:             wl.GetName(),
		Namespace:        wl.GetNamespace(),
		Uid:              string(wl.GetUID()),
		State:            rpcWorkloadState(workload.GetWorkloadState(wl)),
		AgentState:       as,
		InterceptClients: iClients,
	}
}

func (wf *workloadInfoWatcher) handleWorkloadEvents(ctx context.Context, wes []workload.Event, initial bool) {
	if len(wes) == 0 {
		if initial {
			// The initial snapshot may be empty, but must be sent anyway.
			wf.sendEvents(ctx, true)
			return
		}
	} else {
		wf.resetTicker()
	}
	for _, we := range wes {
		wl := we.Workload
		if wf.namespace != "" && wl.GetNamespace() != wf.namespace {
			continue
		}
		wf.workloadEvents.Compute(wl.GetName(), func(w *rpc.WorkloadEvent, loaded bool) (*rpc.WorkloadEvent, xsync.ComputeOp) {
			as := rpc.WorkloadInfo_NO_AGENT_UNSPECIFIED
			if loaded {
				if we.Type == workload.EventTypeDelete && w.Type != rpc.WorkloadEvent_DELETED {
					w = &rpc.WorkloadEvent{
						Type:     rpc.WorkloadEvent_DELETED,
						Workload: w.Workload,
					}
				} else {
					return w, xsync.CancelOp
				}
			} else {
				iClients := wf.getIntercepts(wl.GetName(), wl.GetNamespace())
				if len(iClients) > 0 {
					as = rpc.WorkloadInfo_INTERCEPTED
				} else if wf.HasAgent(wl.GetName(), wl.GetNamespace()) {
					as = rpc.WorkloadInfo_INSTALLED
				}
				w = &rpc.WorkloadEvent{
					Type:     rpc.WorkloadEvent_Type(we.Type),
					Workload: rpcWorkload(wl, as, iClients),
				}
			}
			dlog.Tracef(ctx, "WorkloadInfoEvent: Workload %s %s %s %s", we.Type, wl, as, w.Workload.State)
			return w, xsync.UpdateOp
		})
	}
}

func (wf *workloadInfoWatcher) handleAgentDelta(ctx context.Context, delta cache.Delta[tunnel.SessionID, *AgentSession]) {
	wf.resetTicker()
	m := mutator.GetMap(ctx)
	onDeleted := func(_ tunnel.SessionID, a *AgentSession) {
		name := a.Name
		as := rpc.WorkloadInfo_NO_AGENT_UNSPECIFIED
		wf.workloadEvents.Compute(name, func(w *rpc.WorkloadEvent, loaded bool) (*rpc.WorkloadEvent, xsync.ComputeOp) {
			if loaded {
				if w.Type == rpc.WorkloadEvent_DELETED {
					return w, xsync.CancelOp
				}
				rwl := proto.Clone(w.Workload).(*rpc.WorkloadInfo)
				rwl.AgentState = as
				rwl.InterceptClients = nil
				w = &rpc.WorkloadEvent{
					Type:     w.Type,
					Workload: rwl,
				}
			} else {
				wl, err := agentmap.GetWorkload(ctx, name, a.Namespace, "")
				if err != nil {
					// A delete event will have been generated for this workload.
					return w, xsync.CancelOp
				}
				w = &rpc.WorkloadEvent{
					Type:     rpc.WorkloadEvent_MODIFIED,
					Workload: rpcWorkload(wl, as, nil),
				}
			}
			dlog.Tracef(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, w.Workload.State)
			return w, xsync.UpdateOp
		})
	}
	for k, a := range delta.Removals {
		onDeleted(k, a)
	}

	for k, a := range delta.Upserts {
		if m.IsInactive(types.UID(a.PodUid)) {
			onDeleted(k, a)
			continue
		}
		name := a.Name
		var iClients []*rpc.WorkloadInfo_Intercept
		as := rpc.WorkloadInfo_INSTALLED
		if iis := wf.getIntercepts(name, a.Namespace); len(iis) > 0 {
			as = rpc.WorkloadInfo_INTERCEPTED
			iClients = iis
		}

		wf.workloadEvents.Compute(name, func(w *rpc.WorkloadEvent, loaded bool) (*rpc.WorkloadEvent, xsync.ComputeOp) {
			if loaded {
				if w.Type == rpc.WorkloadEvent_DELETED || w.Workload.AgentState == as {
					return w, xsync.CancelOp
				}
				rwl := proto.Clone(w.Workload).(*rpc.WorkloadInfo)
				rwl.AgentState = as
				rwl.InterceptClients = iClients
				w = &rpc.WorkloadEvent{
					Type:     w.Type,
					Workload: rwl,
				}
			} else {
				wl, err := agentmap.GetWorkload(ctx, name, a.Namespace, "")
				if err != nil {
					// A delete event will have been generated for this workload.
					return w, xsync.CancelOp
				}
				w = &rpc.WorkloadEvent{
					Type:     rpc.WorkloadEvent_ADDED_UNSPECIFIED,
					Workload: rpcWorkload(wl, as, iClients),
				}
			}
			dlog.Tracef(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, w.Workload.State)
			return w, xsync.UpdateOp
		})
	}
}

func (wf *workloadInfoWatcher) handleInterceptDelta(ctx context.Context, delta cache.Delta[string, *Intercept]) {
	wf.resetTicker()

	// Build a map of active intercepts by agent. This map must consider all intercepts, not just those that are provided as upserts.
	// This is faster than calling getIntercepts() for each upserted intercept.
	ipc := make(map[string][]*Intercept)
	wf.intercepts.Range(func(key string, ii *Intercept) bool {
		switch ii.Disposition {
		case rpc.InterceptDispositionType_ACTIVE, rpc.InterceptDispositionType_WAITING, rpc.InterceptDispositionType_NO_AGENT:
			name := ii.Spec.Agent
			ipc[name] = append(ipc[name], ii)
		}
		return true
	})

	makeInterceptClients := func(ics []*Intercept) []*rpc.WorkloadInfo_Intercept {
		iClients := make([]*rpc.WorkloadInfo_Intercept, len(ics))
		for i, ic := range ics {
			iClients[i] = &rpc.WorkloadInfo_Intercept{Client: ic.Spec.Client}
		}
		return iClients
	}

	for _, ii := range delta.Removals {
		name := ii.Spec.Agent
		as := rpc.WorkloadInfo_INSTALLED
		wf.workloadEvents.Compute(name, func(w *rpc.WorkloadEvent, loaded bool) (*rpc.WorkloadEvent, xsync.ComputeOp) {
			if loaded {
				if w.Type == rpc.WorkloadEvent_DELETED {
					return w, xsync.CancelOp
				}
				rwl := proto.Clone(w.Workload).(*rpc.WorkloadInfo)
				rwl.AgentState = as
				rwl.InterceptClients = makeInterceptClients(ipc[name])
				if len(rwl.InterceptClients) > 0 {
					as = rpc.WorkloadInfo_INTERCEPTED
				}
				w = &rpc.WorkloadEvent{
					Type:     w.Type,
					Workload: rwl,
				}
			} else {
				wl, err := agentmap.GetWorkload(ctx, name, wf.namespace, "")
				if err != nil {
					// A delete event will have been generated for this workload.
					return w, xsync.CancelOp
				}
				iClients := makeInterceptClients(ipc[name])
				if len(iClients) > 0 {
					as = rpc.WorkloadInfo_INTERCEPTED
				}
				w = &rpc.WorkloadEvent{
					Type:     rpc.WorkloadEvent_Type(workload.EventTypeUpdate),
					Workload: rpcWorkload(wl, as, iClients),
				}
			}
			dlog.Tracef(ctx, "WorkloadInfoEvent: InterceptInfo %s %s %s", name, as, w.Workload.State)
			return w, xsync.UpdateOp
		})
	}
	if len(delta.Upserts) == 0 {
		return
	}

	for _, ii := range delta.Upserts {
		name := ii.Spec.Agent
		iClients := makeInterceptClients(ipc[name])
		as := rpc.WorkloadInfo_INTERCEPTED
		if len(iClients) > 0 {
			as = rpc.WorkloadInfo_INSTALLED
		}
		wf.workloadEvents.Compute(name, func(w *rpc.WorkloadEvent, loaded bool) (*rpc.WorkloadEvent, xsync.ComputeOp) {
			if loaded {
				if w.Type == rpc.WorkloadEvent_DELETED {
					return w, xsync.CancelOp
				}
				rwl := proto.Clone(w.Workload).(*rpc.WorkloadInfo)
				rwl.AgentState = as
				rwl.InterceptClients = iClients
				w = &rpc.WorkloadEvent{
					Type:     w.Type,
					Workload: rwl,
				}
			} else {
				wl, err := agentmap.GetWorkload(ctx, name, wf.namespace, "")
				if err != nil {
					return nil, xsync.CancelOp
				}
				w = &rpc.WorkloadEvent{
					Type:     rpc.WorkloadEvent_Type(workload.EventTypeUpdate),
					Workload: rpcWorkload(wl, as, iClients),
				}
			}
			dlog.Tracef(ctx, "WorkloadInfoEvent: InterceptInfo %s.%s %s %s", w.Workload.Name, w.Workload.Namespace, as, w.Workload.State)
			return w, xsync.UpdateOp
		})
	}
}
