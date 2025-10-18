package state

import (
	"context"
	"math"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
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
	workloadEvents map[string]*rpc.WorkloadEvent
	lastEvents     map[string]*rpc.WorkloadEvent
	agentInfos     map[tunnel.SessionID]*AgentSession
	interceptInfos map[string]*Intercept
	start          time.Time
	ticker         *time.Ticker
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
	wf.ticker = time.NewTicker(time.Duration(math.MaxInt64))
	defer func() {
		wf.ticker.Stop()
		wf.stream = nil
		wf.lastEvents = nil
		wf.agentInfos = nil
		wf.interceptInfos = nil
		wf.workloadEvents = nil
	}()

	wf.stream = stream
	wf.workloadEvents = make(map[string]*rpc.WorkloadEvent)

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
		case <-wf.ticker.C:
			wf.sendEvents(ctx, false)
		case wes, ok := <-workloadsCh:
			if !ok {
				dlog.Debug(ctx, "Workloads channel closed")
				return nil
			}
			wf.handleWorkloadsSnapshot(ctx, wes, initial)
			initial = false
		// Events that arrive at the agent channel should be counted as modifications.
		case ais, ok := <-agentsCh:
			if !ok {
				dlog.Debug(ctx, "Agents channel closed")
				return nil
			}
			wf.handleAgentSnapshot(ctx, ais)
		// Events that arrive at the intercept channel should be counted as modifications.
		case is, ok := <-interceptsCh:
			if !ok {
				dlog.Debug(ctx, "Intercepts channel closed")
				return nil
			}
			wf.handleInterceptSnapshot(ctx, is)
		}
	}
}

func (wf *workloadInfoWatcher) getIntercepts(name, namespace string) (iis []*rpc.WorkloadInfo_Intercept) {
	for _, ii := range wf.interceptInfos {
		if name == ii.Spec.Agent && namespace == ii.Spec.Namespace && ii.Disposition == rpc.InterceptDispositionType_ACTIVE {
			iis = append(iis, &rpc.WorkloadInfo_Intercept{
				Client: ii.Spec.Client,
			})
		}
	}
	return iis
}

func (wf *workloadInfoWatcher) sendEvents(ctx context.Context, sendEmpty bool) {
	// Time to send what we have
	wf.ticker.Reset(time.Duration(math.MaxInt64))
	evs := make([]*rpc.WorkloadEvent, 0, len(wf.workloadEvents))
	for k, rew := range wf.workloadEvents {
		if lew, ok := wf.lastEvents[k]; ok {
			if proto.Equal(lew, rew) {
				continue
			}
		}
		evs = append(evs, rew)
	}
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
	wf.lastEvents = wf.workloadEvents
	wf.workloadEvents = make(map[string]*rpc.WorkloadEvent)
	wf.start = time.Now()
}

func (wf *workloadInfoWatcher) resetTicker() {
	wf.ticker.Reset(5 * time.Millisecond)
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

func extractPortsFromSidecar(_ context.Context, sc *agentconfig.Sidecar) []*rpc.WorkloadPortInfo {
	if sc == nil {
		return nil
	}

	var ports []*rpc.WorkloadPortInfo
	seenPorts := make(map[types.PortAndProto]bool) // Track seen port+protocol combinations to avoid duplicates

	// Extract ports from all containers in the sidecar
	for _, container := range sc.Containers {
		for _, intercept := range container.Intercepts {
			// Create a unique key using container port number and protocol
			// This allows the same port with different protocols (TCP/UDP)
			// We use container port because service port may not be present for headless workloads
			portKey := types.PortAndProto{
				Port:  intercept.ContainerPort,
				Proto: intercept.Protocol,
			}

			if !seenPorts[portKey] {
				seenPorts[portKey] = true
				protocol := intercept.Protocol.String()
				ports = append(ports, &rpc.WorkloadPortInfo{
					ContainerPortName: intercept.ContainerPortName,
					ContainerPort:     int32(intercept.ContainerPort),
					Protocol:          protocol,
					ServicePortName:   intercept.ServicePortName,
					ServicePort:       int32(intercept.ServicePort),
				})
			}
		}
	}

	return ports
}

func extractPortsForWorkload(ctx context.Context, wl k8sapi.Workload) []*rpc.WorkloadPortInfo {
	// Check if we already have a sidecar config from an installed traffic-agent
	m := mutator.GetMap(ctx)
	if m == nil {
		dlog.Debugf(ctx, "mutator map not available, cannot discover ports for %s", wl)
		return nil
	}

	sc := m.Get(wl.GetName(), wl.GetNamespace())
	if sc == nil {
		// No existing sidecar, generate a new one to discover ports
		// Only attempt generation if an agent image has been configured (i.e., agent injector is enabled)
		if ir := managerutil.GetAgentImageRetriever(ctx); ir != nil {
			agentImage := ir.GetImage()

			// Generate the sidecar config which properly maps service ports to container ports
			gc, err := managerutil.GetEnv(ctx).GeneratorConfig(agentImage)
			if err != nil {
				dlog.Warnf(ctx, "failed to get generator config for %s: %v", wl, err)
				return nil
			}

			sc, err = gc.Generate(ctx, wl, nil)
			if err != nil {
				dlog.Debugf(ctx, "failed to generate sidecar config for %s: %v", wl, err)
				return nil
			}
		} else {
			// Agent injector is not enabled, unable to discover ports
			dlog.Debugf(ctx, "agent injector not enabled, unable to discover ports for %s", wl)
			return nil
		}
	}

	return extractPortsFromSidecar(ctx, sc)
}

func (wf *workloadInfoWatcher) rpcWorkload(wl k8sapi.Workload, as rpc.WorkloadInfo_AgentState, iClients []*rpc.WorkloadInfo_Intercept) *rpc.WorkloadInfo {
	var ports []*rpc.WorkloadPortInfo
	if wf.State != nil && wf.backgroundCtx != nil {
		ports = extractPortsForWorkload(wf.backgroundCtx, wl)
	}
	return &rpc.WorkloadInfo{
		Kind:             workload.RpcKind(wl.GetKind()),
		Name:             wl.GetName(),
		Namespace:        wl.GetNamespace(),
		Uid:              string(wl.GetUID()),
		State:            rpcWorkloadState(workload.GetWorkloadState(wl)),
		AgentState:       as,
		InterceptClients: iClients,
		Ports:            ports,
	}
}

func (wf *workloadInfoWatcher) addEvent(
	eventType EventType,
	wl k8sapi.Workload,
	as rpc.WorkloadInfo_AgentState,
	iClients []*rpc.WorkloadInfo_Intercept,
) {
	wf.workloadEvents[wl.GetName()] = &rpc.WorkloadEvent{
		Type:     rpc.WorkloadEvent_Type(eventType),
		Workload: wf.rpcWorkload(wl, as, iClients),
	}
	wf.resetTicker()
}

func (wf *workloadInfoWatcher) handleWorkloadsSnapshot(ctx context.Context, wes []Event, initial bool) {
	if len(wes) == 0 {
		if initial {
			// The initial snapshot may be empty, but must be sent anyway.
			wf.sendEvents(ctx, true)
		}
		return
	}
	for _, we := range wes {
		wl := we.Workload
		if wf.namespace != "" && wl.GetNamespace() != wf.namespace {
			continue
		}
		if w, ok := wf.workloadEvents[wl.GetName()]; ok {
			if we.Type == EventTypeDelete && w.Type != rpc.WorkloadEvent_DELETED {
				w.Type = rpc.WorkloadEvent_DELETED
				dlog.Debugf(ctx, "WorkloadInfoEvent: Workload %s %s %s", we.Type, wl, workload.GetWorkloadState(wl))
				wf.resetTicker()
			}
		} else {
			var iClients []*rpc.WorkloadInfo_Intercept
			as := rpc.WorkloadInfo_NO_AGENT_UNSPECIFIED
			if wf.HasAgent(wl.GetName(), wl.GetNamespace()) {
				if iis := wf.getIntercepts(wl.GetName(), wl.GetNamespace()); len(iis) > 0 {
					as = rpc.WorkloadInfo_INTERCEPTED
					iClients = iis
				} else {
					as = rpc.WorkloadInfo_INSTALLED
				}
			}

			// If we've sent an ADDED event for this workload, and this is a MODIFIED event without any changes that
			// we care about, then just skip it.
			if we.Type == EventTypeUpdate {
				lew, ok := wf.lastEvents[wl.GetName()]
				if ok && (lew.Type == rpc.WorkloadEvent_ADDED_UNSPECIFIED || lew.Type == rpc.WorkloadEvent_MODIFIED) &&
					proto.Equal(lew.Workload, wf.rpcWorkload(we.Workload, as, iClients)) {
					break
				}
			}
			dlog.Debugf(ctx, "WorkloadInfoEvent: Workload %s %s %s %s", we.Type, wl, as, workload.GetWorkloadState(wl))
			wf.addEvent(we.Type, wl, as, iClients)
		}
	}
}

func (wf *workloadInfoWatcher) handleAgentSnapshot(ctx context.Context, ais map[tunnel.SessionID]*AgentSession) {
	oldAgentInfos := wf.agentInfos
	wf.agentInfos = ais
	m := mutator.GetMap(ctx)
	for k, a := range oldAgentInfos {
		ai, ok := ais[k]
		if !ok || m.IsInactive(k8stypes.UID(ai.PodUid)) {
			name := a.Name
			as := rpc.WorkloadInfo_NO_AGENT_UNSPECIFIED
			if w, ok := wf.workloadEvents[name]; ok && w.Type != rpc.WorkloadEvent_DELETED {
				wl := w.Workload
				if wl.AgentState != as {
					wl.AgentState = as
					dlog.Debugf(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, wl.State)
					wf.resetTicker()
				}
			} else if wl, err := agentmap.GetWorkload(ctx, name, a.Namespace, ""); err == nil {
				dlog.Debugf(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, workload.GetWorkloadState(wl))
				wf.addEvent(EventTypeUpdate, wl, as, nil)
			} else {
				dlog.Debugf(ctx, "Unable to get workload %s.%s: %v", name, a.Namespace, err)
				if errors.IsNotFound(err) {
					wf.workloadEvents[name] = &rpc.WorkloadEvent{
						Type: rpc.WorkloadEvent_DELETED,
						Workload: &rpc.WorkloadInfo{
							Name:       name,
							Namespace:  a.Namespace,
							AgentState: as,
						},
					}
					wf.sendEvents(ctx, false)
				}
			}
		}
	}
	for _, a := range ais {
		if m.IsInactive(k8stypes.UID(a.PodUid)) {
			continue
		}
		name := a.Name
		var iClients []*rpc.WorkloadInfo_Intercept
		as := rpc.WorkloadInfo_INSTALLED
		if iis := wf.getIntercepts(name, a.Namespace); len(iis) > 0 {
			as = rpc.WorkloadInfo_INTERCEPTED
			iClients = iis
		}
		if w, ok := wf.workloadEvents[name]; ok && w.Type != rpc.WorkloadEvent_DELETED {
			wl := w.Workload
			dlog.Debugf(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, w.Workload.State)
			if wl.AgentState != as {
				wl.AgentState = as
				wl.InterceptClients = iClients
				wf.resetTicker()
			}
		} else if wl, err := agentmap.GetWorkload(ctx, name, a.Namespace, ""); err == nil {
			dlog.Debugf(ctx, "WorkloadInfoEvent: AgentInfo %s(%s).%s %s %s", a.PodName, a.PodIp, a.Namespace, as, workload.GetWorkloadState(wl))
			wf.addEvent(EventTypeUpdate, wl, as, iClients)
		} else {
			dlog.Debugf(ctx, "Unable to get workload %s.%s: %v", name, a.Namespace, err)
		}
	}
}

func (wf *workloadInfoWatcher) handleInterceptSnapshot(ctx context.Context, iis map[string]*Intercept) {
	oldInterceptInfos := wf.interceptInfos
	wf.interceptInfos = iis
	for k, ii := range oldInterceptInfos {
		if _, ok := wf.interceptInfos[k]; !ok {
			name := ii.Spec.Agent
			as := rpc.WorkloadInfo_INSTALLED
			if w, ok := wf.workloadEvents[name]; ok && w.Type != rpc.WorkloadEvent_DELETED {
				if w.Workload.AgentState != as {
					w.Workload.AgentState = as
					w.Workload.InterceptClients = nil
					dlog.Debugf(ctx, "WorkloadInfoEvent: InterceptInfo %s.%s %s %s", w.Workload.Name, w.Workload.Namespace, as, w.Workload.State)
					wf.resetTicker()
				}
			} else if wl, err := agentmap.GetWorkload(ctx, name, wf.namespace, ""); err == nil {
				dlog.Debugf(ctx, "WorkloadInfoEvent: InterceptInfo %s %s %s", wl, as, workload.GetWorkloadState(wl))
				wf.addEvent(EventTypeUpdate, wl, as, nil)
			}
		}
	}
	ipc := make(map[string][]*Intercept)
	for _, ii := range wf.interceptInfos {
		name := ii.Spec.Agent
		if ii.Disposition == rpc.InterceptDispositionType_ACTIVE {
			ipc[name] = append(ipc[name], ii)
		}
	}
	for name, iis := range ipc {
		iClients := make([]*rpc.WorkloadInfo_Intercept, len(iis))
		as := rpc.WorkloadInfo_INTERCEPTED
		for i, ii := range iis {
			iClients[i] = &rpc.WorkloadInfo_Intercept{Client: ii.Spec.Client}
		}
		if w, ok := wf.workloadEvents[name]; ok && w.Type != rpc.WorkloadEvent_DELETED {
			if w.Workload.AgentState != as {
				w.Workload.AgentState = as
				w.Workload.InterceptClients = iClients
				dlog.Debugf(ctx, "WorkloadInfoEvent: InterceptInfo %s.%s %s %s", w.Workload.Name, w.Workload.Namespace, as, w.Workload.State)
				wf.resetTicker()
			}
		} else if wl, err := agentmap.GetWorkload(ctx, name, wf.namespace, ""); err == nil {
			dlog.Debugf(ctx, "WorkloadInfoEvent: InterceptInfo %s %s %s", wl, as, workload.GetWorkloadState(wl))
			wf.addEvent(EventTypeUpdate, wl, as, iClients)
		}
	}
}
