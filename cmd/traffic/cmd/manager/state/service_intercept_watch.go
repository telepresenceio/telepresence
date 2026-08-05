package state

import (
	"context"
	"errors"
	"fmt"
	"strings"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (s *State) startServiceInterceptWatch(interceptID string) {
	if s.serviceInterceptWatchers == nil {
		return
	}
	if _, loaded := s.serviceInterceptWatchers.LoadOrStore(interceptID, struct{}{}); loaded {
		return
	}
	intercept, ok := s.intercepts.Load(interceptID)
	if !ok || !serviceScopedIntercept(intercept.Spec) {
		s.serviceInterceptWatchers.Delete(interceptID)
		return
	}

	ctx, cancel := context.WithCancel(s.backgroundCtx)
	if err := s.AddInterceptFinalizer(interceptID, func(context.Context, *rpc.InterceptInfo) error {
		cancel()
		return nil
	}); err != nil {
		cancel()
		s.serviceInterceptWatchers.Delete(interceptID)
		clog.Errorf(s.backgroundCtx, "Failed to start Service intercept watcher for %s: %v", interceptID, err)
		return
	}
	go func() {
		defer cancel()
		defer s.serviceInterceptWatchers.Delete(interceptID)
		s.watchServiceInterceptWorkloads(ctx, interceptID)
	}()
}

func serviceMatchesIntercept(obj any, spec *rpc.InterceptSpec) bool {
	svc, ok := obj.(*core.Service)
	return ok && svc.Namespace == spec.Namespace && svc.Name == spec.ServiceName
}

func podMatchesServiceSelector(obj any, svc *core.Service, spec *rpc.InterceptSpec) bool {
	if deleted, ok := obj.(*k8sCache.DeletedFinalStateUnknown); ok {
		obj = deleted.Obj
	}
	pod, ok := obj.(*core.Pod)
	return ok && pod.Namespace == spec.Namespace && len(svc.Spec.Selector) > 0 &&
		labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(pod.Labels))
}

func podUpdateMayAffectService(oldObj, newObj any, svc *core.Service, spec *rpc.InterceptSpec) bool {
	oldPod, oldOK := oldObj.(*core.Pod)
	newPod, newOK := newObj.(*core.Pod)
	if !oldOK || !newOK {
		return false
	}
	oldMatches := podMatchesServiceSelector(oldPod, svc, spec)
	newMatches := podMatchesServiceSelector(newPod, svc, spec)
	if oldMatches != newMatches {
		return true
	}
	return oldMatches && servicePodIsRelevant(oldPod) != servicePodIsRelevant(newPod)
}

func (s *State) watchServiceInterceptWorkloads(ctx context.Context, interceptID string) {
	intercept, ok := s.intercepts.Load(interceptID)
	if !ok || !serviceScopedIntercept(intercept.Spec) {
		return
	}
	spec := intercept.Spec
	f := informer.GetK8sFactory(ctx, spec.Namespace)
	if f == nil {
		clog.Debugf(ctx, "No informer factory available for Service intercept %q", interceptID)
		return
	}

	reconcileEvents := make(chan struct{}, 1)
	signal := func() {
		select {
		case reconcileEvents <- struct{}{}:
		default:
		}
	}
	notifyService := func(obj any) {
		if !serviceMatchesIntercept(obj, spec) {
			return
		}
		signal()
	}
	reg, err := f.Core().V1().Services().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc: notifyService,
		UpdateFunc: func(_, newObj any) {
			notifyService(newObj)
		},
		DeleteFunc: func(obj any) {
			if deleted, ok := obj.(*k8sCache.DeletedFinalStateUnknown); ok {
				notifyService(deleted.Obj)
				return
			}
			notifyService(obj)
		},
	})
	if err != nil {
		clog.Errorf(ctx, "Unable to watch Service changes for intercept %q: %v", interceptID, err)
		return
	}
	defer func() {
		_ = f.Core().V1().Services().Informer().RemoveEventHandler(reg)
	}()

	currentService := func() *core.Service {
		svc, err := f.Core().V1().Services().Lister().Services(spec.Namespace).Get(spec.ServiceName)
		if err != nil || string(svc.UID) != spec.ServiceUid {
			return nil
		}
		return svc
	}
	notifyPod := func(obj any) {
		if svc := currentService(); svc != nil && podMatchesServiceSelector(obj, svc, spec) {
			signal()
		}
	}
	podReg, err := f.Core().V1().Pods().Informer().AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
		AddFunc: notifyPod,
		UpdateFunc: func(oldObj, newObj any) {
			if svc := currentService(); svc != nil && podUpdateMayAffectService(oldObj, newObj, svc, spec) {
				signal()
			}
		},
		DeleteFunc: notifyPod,
	})
	if err != nil {
		clog.Errorf(ctx, "Unable to watch Pod changes for intercept %q: %v", interceptID, err)
		return
	}
	defer func() {
		_ = f.Core().V1().Pods().Informer().RemoveEventHandler(podReg)
	}()

	workloadEvents, err := s.WatchWorkloads(ctx, spec.Namespace)
	if err != nil {
		clog.Errorf(ctx, "Unable to watch workload changes for intercept %q: %v", interceptID, err)
		return
	}
	s.reconcileServiceInterceptWorkloads(ctx, interceptID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileEvents:
			s.reconcileServiceInterceptWorkloads(ctx, interceptID)
		case events := <-workloadEvents:
			if events == nil {
				return
			}
			s.reconcileServiceInterceptWorkloads(ctx, interceptID)
		}
	}
}

// reconcileServiceInterceptWorkloads provisions newly selected workloads
// before waiting for their agents to join the logical intercept.
func (s *State) reconcileServiceInterceptWorkloads(ctx context.Context, interceptID string) {
	intercept, ok := s.intercepts.Load(interceptID)
	if !ok || !serviceScopedIntercept(intercept.Spec) {
		return
	}
	workloads, known, err := discoverServiceWorkloads(ctx, intercept.Spec)
	if err != nil {
		if errors.Is(err, errServicePodOnlySelection) ||
			errors.Is(err, errServicePodOwnerUnrepresentable) ||
			errors.Is(err, errServiceIdentityChanged) ||
			errors.Is(err, errServiceSelectorless) {
			s.fallbackServiceInterceptToSingleWorkload(interceptID, err.Error())
			return
		}
		clog.Debugf(ctx, "Unable to discover workloads for intercept %q: %v", interceptID, err)
		return
	}
	if !known {
		return
	}
	selected := serviceParticipantsFromWorkloads(workloads)

	mm := mutator.GetMap(ctx)
	if mm == nil {
		s.reconcileServiceParticipantsWithSelection(interceptID, intercept, selected, true, false)
		return
	}
	var client *ClientSession
	if intercept.ClientSession != nil {
		client = s.GetClient(tunnel.SessionID(intercept.ClientSession.SessionId))
	}
	// Validate every secondary before publishing it as a participant. In
	// particular, an existing config can already claim this Service while a
	// different active intercept still makes the expansion unsafe.
	for _, workload := range workloads {
		if workload.GetName() == intercept.Spec.Agent &&
			(intercept.Spec.WorkloadKind == "" || string(workload.GetKind()) == intercept.Spec.WorkloadKind) {
			continue
		}
		if err = s.checkServiceWorkloadConflictsIgnoring(ctx, intercept.Spec, workload, client, interceptID); err != nil {
			s.fallbackServiceInterceptToSingleWorkload(interceptID,
				fmt.Sprintf("workload %s conflicts with an existing intercept: %v", workload, err))
			return
		}
	}

	intercept = s.reconcileServiceParticipantsWithSelection(interceptID, intercept, selected, true, true)
	if intercept == nil || !serviceScopedIntercept(intercept.Spec) {
		return
	}
	for _, workload := range workloads {
		if workload.GetName() == intercept.Spec.Agent &&
			(intercept.Spec.WorkloadKind == "" || string(workload.GetKind()) == intercept.Spec.WorkloadKind) {
			continue
		}
		config := mm.Get(workload.GetName(), workload.GetNamespace())
		if config != nil && sidecarClaimsService(config, intercept.Spec) {
			continue
		}
		config, err = s.getOrCreateAgentConfig(ctx, workload, s.isExtended(intercept.Spec), false,
			intercept.Spec, agentconfig.ReplacePolicyIntercept)
		if err != nil {
			clog.Errorf(ctx, "Unable to provision workload %s for intercept %q: %v", workload, interceptID, err)
			continue
		}
		if !sidecarClaimsService(config, intercept.Spec) {
			clog.Errorf(ctx, "Agent config for workload %s does not claim the selected Service target", workload)
			continue
		}
		if err = mm.EvictPodsWithAgentConfigMismatch(ctx, workload, config); err != nil {
			clog.Errorf(ctx, "Unable to refresh workload %s for intercept %q: %v", workload, interceptID, err)
		}
	}

	intercept, ok = s.intercepts.Load(interceptID)
	if !ok {
		return
	}
	if errCode, errMsg := s.checkAgentsForIntercept(intercept); errCode != rpc.InterceptDispositionType_UNSPECIFIED {
		s.UpdateIntercept(interceptID, func(intercept *Intercept) {
			intercept.Disposition = errCode
			intercept.Message = errMsg
		})
	} else if intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT {
		s.UpdateIntercept(interceptID, func(intercept *Intercept) {
			intercept.Disposition = rpc.InterceptDispositionType_WAITING
			intercept.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s",
				strings.Join(intercept.pendingParticipants(), ", "))
		})
	}
}

// fallbackServiceInterceptToSingleWorkload stops shared-Service expansion
// when a live Service endpoint cannot be represented safely by a
// workload-scoped agent config. The requested workload keeps workload-scoped
// interception, and participant-aware delivery removes the intercept from
// any secondary agents.
func (s *State) fallbackServiceInterceptToSingleWorkload(interceptID, reason string) {
	s.UpdateIntercept(interceptID, func(intercept *Intercept) {
		if !serviceScopedIntercept(intercept.Spec) {
			return
		}
		needsSingleWorkloadReview := intercept.Disposition == rpc.InterceptDispositionType_NO_AGENT ||
			intercept.Disposition == rpc.InterceptDispositionType_AGENT_ERROR
		fallbackToSingleWorkload(s.backgroundCtx, intercept.Spec, reason)
		intercept.participants = nil
		intercept.syncServiceWorkloads()
		if !needsSingleWorkloadReview {
			return
		}
		if errCode, errMsg := s.checkAgentsForIntercept(intercept); errCode != rpc.InterceptDispositionType_UNSPECIFIED {
			intercept.Disposition = errCode
			intercept.Message = errMsg
			return
		}
		intercept.Disposition = rpc.InterceptDispositionType_WAITING
		intercept.Message = "Waiting for Agent approval"
	})
}
