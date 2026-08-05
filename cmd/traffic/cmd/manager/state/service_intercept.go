package state

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// interceptParticipant tracks one workload that must approve a service-scoped
// intercept before the logical intercept can become active. A workload may
// have many agent pods, but one approving pod is sufficient because all pods
// receive the active intercept snapshot.
type interceptParticipant struct {
	namespace string
	kind      string
	name      string
	podName   string
	review    *rpc.ReviewInterceptRequest
}

func (p *interceptParticipant) clone() *interceptParticipant {
	cp := &interceptParticipant{
		namespace: p.namespace,
		kind:      p.kind,
		name:      p.name,
		podName:   p.podName,
	}
	if p.review != nil {
		cp.review = proto.Clone(p.review).(*rpc.ReviewInterceptRequest)
	}
	return cp
}

func participantKey(namespace, kind, name string) string {
	return namespace + "/" + kind + "/" + name
}

func agentParticipantKey(agent *rpc.AgentInfo) string {
	return participantKey(agent.Namespace, agent.Kind, agent.Name)
}

func serviceScopedIntercept(spec *rpc.InterceptSpec) bool {
	return spec != nil && spec.ServiceUid != "" && (spec.ServicePort > 0 || spec.ServicePortName != "")
}

func servicePortMatches(target *rpc.AgentInfo_InterceptTarget, spec *rpc.InterceptSpec) bool {
	if target.ServiceUid != spec.ServiceUid || target.Protocol != spec.Protocol {
		return false
	}
	if spec.ServicePort > 0 {
		return target.ServicePort == spec.ServicePort
	}
	return target.ServicePortName == spec.ServicePortName
}

func agentClaimsService(agent *rpc.AgentInfo, spec *rpc.InterceptSpec) bool {
	for _, target := range agent.InterceptTargets {
		if servicePortMatches(target, spec) {
			return true
		}
	}
	return false
}

func allAgentsMatchIntercept(agents []*rpc.AgentInfo, spec *rpc.InterceptSpec) bool {
	for _, agent := range agents {
		if !AgentMatchesIntercept(agent, spec) {
			return false
		}
	}
	return true
}

// AgentMatchesIntercept reports whether an agent is eligible to receive and
// review an intercept. Workload-scoped intercepts match by requested workload
// name. Service-backed intercepts match advertised Service UID and port claims.
func AgentMatchesIntercept(agent *rpc.AgentInfo, spec *rpc.InterceptSpec) bool {
	if agent == nil || spec == nil || agent.Namespace != spec.Namespace {
		return false
	}
	if !serviceScopedIntercept(spec) {
		return agent.Name == spec.Agent
	}
	if agentClaimsService(agent, spec) {
		return true
	}
	// Agents with no advertised targets match only the requested workload.
	return len(agent.InterceptTargets) == 0 && agent.Name == spec.Agent
}

// AgentMatchesInterceptInfo reports whether an agent both matches an
// intercept's target and belongs to its current participant set. Service
// selectors can drop a workload while an old agent still advertises the
// Service target, so delivery must use the participant-aware predicate.
func AgentMatchesInterceptInfo(agent *rpc.AgentInfo, intercept *Intercept) bool {
	if intercept == nil || !AgentMatchesIntercept(agent, intercept.Spec) {
		return false
	}
	if !serviceScopedIntercept(intercept.Spec) {
		return true
	}
	_, ok := intercept.participants[agentParticipantKey(agent)]
	return ok
}

func participantsEqual(a, b map[string]*interceptParticipant) bool {
	if len(a) != len(b) {
		return false
	}
	for key, ap := range a {
		bp, ok := b[key]
		if !ok || ap.namespace != bp.namespace || ap.kind != bp.kind || ap.name != bp.name ||
			ap.podName != bp.podName || !proto.Equal(ap.review, bp.review) {
			return false
		}
	}
	return true
}

func (is *Intercept) cloneParticipants() map[string]*interceptParticipant {
	if len(is.participants) == 0 {
		return nil
	}
	participants := make(map[string]*interceptParticipant, len(is.participants))
	for key, participant := range is.participants {
		participants[key] = participant.clone()
	}
	return participants
}

func (s *State) initializeParticipants(intercept *Intercept) {
	if !serviceScopedIntercept(intercept.Spec) {
		return
	}
	intercept.participants = make(map[string]*interceptParticipant)
	// Restored intercepts already carry the workload identities that took
	// part before the manager restarted. Seed those first so a reconnecting
	// client cannot observe an empty participant set while agents are
	// arriving again.
	for _, workload := range intercept.ServiceWorkloads {
		if workload == nil || workload.WorkloadName == "" {
			continue
		}
		key := participantKey(workload.Namespace, workload.WorkloadKind, workload.WorkloadName)
		intercept.participants[key] = &interceptParticipant{
			namespace: workload.Namespace,
			kind:      workload.WorkloadKind,
			name:      workload.WorkloadName,
		}
	}
	persistedKeys := make([]string, 0, len(intercept.participants))
	for key := range intercept.participants {
		persistedKeys = append(persistedKeys, key)
	}
	// Persisted intercepts may not carry ServiceWorkloads. The
	// explicitly requested workload is safe to restore, but newly observed
	// secondary workloads must wait for watcher preflight before joining.
	primaryKey := participantKey(intercept.Spec.Namespace, intercept.Spec.WorkloadKind, intercept.Spec.Agent)
	if _, ok := intercept.participants[primaryKey]; !ok {
		intercept.participants[primaryKey] = &interceptParticipant{
			namespace: intercept.Spec.Namespace,
			kind:      intercept.Spec.WorkloadKind,
			name:      intercept.Spec.Agent,
		}
	}
	// On restore, RestoreAgents runs before RestoreIntercepts, so use the
	// recorded pod to reconnect the published review to its workload before
	// current selection can prune it. Without a matching restored agent, only
	// a single persisted participant (or a snapshot with none) is safe
	// to infer; otherwise wait for fresh reviews instead of guessing.
	hasPublishedPod := intercept.PodName != "" || intercept.PodIp != ""
	publishedKey := ""
	if hasPublishedPod {
		s.EachAgent(func(_ tunnel.SessionID, agent *AgentSession) bool {
			if agent.Namespace != intercept.Spec.Namespace ||
				(intercept.PodName != "" && agent.PodName != intercept.PodName) ||
				(intercept.PodName == "" && intercept.PodIp != "" && agent.PodIp != intercept.PodIp) {
				return true
			}
			key := agentParticipantKey(agent.AgentInfo)
			if _, ok := intercept.participants[key]; ok {
				publishedKey = key
			}
			return true
		})
		if publishedKey == "" {
			switch len(persistedKeys) {
			case 0:
				publishedKey = primaryKey
			case 1:
				publishedKey = persistedKeys[0]
			}
		}
	} else if published := intercept.publishedServiceParticipant(); published != nil {
		publishedKey = participantKey(published.namespace, published.kind, published.name)
	}
	publishedUnknown := hasPublishedPod && publishedKey == ""
	prunedPublished := false
	for _, key := range s.staleServiceParticipantKeys(intercept) {
		prunedPublished = prunedPublished || key == publishedKey
		delete(intercept.participants, key)
	}
	if !prunedPublished && !publishedUnknown && intercept.PodName != "" {
		if participant, ok := intercept.participants[publishedKey]; ok {
			participant.podName = intercept.PodName
		}
	}
	intercept.syncServiceWorkloads()
	if prunedPublished || publishedUnknown {
		intercept.reselectServiceReview()
	}
}

func (is *Intercept) addParticipant(agent *rpc.AgentInfo) *interceptParticipant {
	key := agentParticipantKey(agent)
	if participant, ok := is.participants[key]; ok {
		return participant
	}
	participant := &interceptParticipant{
		namespace: agent.Namespace,
		kind:      agent.Kind,
		name:      agent.Name,
	}
	if is.participants == nil {
		is.participants = make(map[string]*interceptParticipant)
	}
	is.participants[key] = participant
	is.syncServiceWorkloads()
	return participant
}

func (is *Intercept) syncServiceWorkloads() {
	if len(is.participants) == 0 {
		is.ServiceWorkloads = nil
		return
	}
	keys := is.participantKeys()
	workloads := make([]*rpc.InterceptWorkload, 0, len(keys))
	for _, key := range keys {
		participant := is.participants[key]
		workloads = append(workloads, &rpc.InterceptWorkload{
			Namespace:    participant.namespace,
			WorkloadKind: participant.kind,
			WorkloadName: participant.name,
		})
	}
	is.ServiceWorkloads = workloads
}

func (is *Intercept) pendingParticipants() []string {
	pending := make([]string, 0, len(is.participants))
	for _, participant := range is.participants {
		if participant.review == nil {
			pending = append(pending, participant.name)
		}
	}
	sort.Strings(pending)
	return pending
}

func (is *Intercept) participantKeys() []string {
	keys := make([]string, 0, len(is.participants))
	for key := range is.participants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *State) agentsByParticipant(intercept *Intercept) map[string][]*rpc.AgentInfo {
	groups := make(map[string][]*rpc.AgentInfo)
	if !serviceScopedIntercept(intercept.Spec) {
		key := participantKey(intercept.Spec.Namespace, intercept.Spec.WorkloadKind, intercept.Spec.Agent)
		s.EachAgent(func(_ tunnel.SessionID, agent *AgentSession) bool {
			if AgentMatchesIntercept(agent.AgentInfo, intercept.Spec) {
				groups[key] = append(groups[key], agent.AgentInfo)
			}
			return true
		})
		return groups
	}

	for key := range intercept.participants {
		groups[key] = nil
	}
	s.EachAgent(func(_ tunnel.SessionID, agent *AgentSession) bool {
		key := agentParticipantKey(agent.AgentInfo)
		if _, ok := intercept.participants[key]; ok {
			groups[key] = append(groups[key], agent.AgentInfo)
		}
		return true
	})
	return groups
}

func serviceParticipantsFromWorkloads(workloads []k8sapi.Workload) map[string]*interceptParticipant {
	selected := make(map[string]*interceptParticipant)
	for _, workload := range workloads {
		key := participantKey(workload.GetNamespace(), string(workload.GetKind()), workload.GetName())
		selected[key] = &interceptParticipant{
			namespace: workload.GetNamespace(),
			kind:      string(workload.GetKind()),
			name:      workload.GetName(),
		}
	}
	return selected
}

func (s *State) selectedServiceParticipants(intercept *Intercept) (map[string]*interceptParticipant, bool) {
	workloads, known, err := discoverServiceWorkloads(s.backgroundCtx, intercept.Spec)
	if err != nil {
		clog.Debugf(s.backgroundCtx, "Unable to reconcile participants for intercept %q: %v", intercept.Id, err)
		return nil, false
	}
	if !known {
		return nil, false
	}
	return serviceParticipantsFromWorkloads(workloads), true
}

// staleServiceParticipantKeys retains zero-agent workloads only while their
// templates still match the Service selector.
func (s *State) staleServiceParticipantKeys(intercept *Intercept) []string {
	if !serviceScopedIntercept(intercept.Spec) || len(intercept.participants) == 0 {
		return nil
	}
	selected, selectionKnown := s.selectedServiceParticipants(intercept)
	return s.staleServiceParticipantKeysWithSelection(intercept, selected, selectionKnown)
}

func (s *State) staleServiceParticipantKeysWithSelection(
	intercept *Intercept,
	selected map[string]*interceptParticipant,
	selectionKnown bool,
) []string {
	if !serviceScopedIntercept(intercept.Spec) || len(intercept.participants) == 0 {
		return nil
	}
	present := make(map[string]bool)
	matching := make(map[string]bool)
	s.EachAgent(func(_ tunnel.SessionID, agent *AgentSession) bool {
		key := agentParticipantKey(agent.AgentInfo)
		if _, ok := intercept.participants[key]; !ok {
			return true
		}
		present[key] = true
		if AgentMatchesIntercept(agent.AgentInfo, intercept.Spec) {
			matching[key] = true
		}
		return true
	})

	stale := make(map[string]struct{})
	for key := range intercept.participants {
		if selectionKnown {
			if _, ok := selected[key]; !ok {
				stale[key] = struct{}{}
			}
			continue
		}
		if present[key] && !matching[key] {
			stale[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(stale))
	for key := range stale {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// reconcileServiceParticipants prunes workloads that no longer belong to the
// Service. Newly selected workloads are added only by the Service watcher
// after it has preflighted the exact selection snapshot.
func (s *State) reconcileServiceParticipants(interceptID string, intercept *Intercept) *Intercept {
	selected, selectionKnown := s.selectedServiceParticipants(intercept)
	return s.reconcileServiceParticipantsWithSelection(interceptID, intercept, selected, selectionKnown, false)
}

func (s *State) reconcileServiceParticipantsWithSelection(
	interceptID string,
	intercept *Intercept,
	selected map[string]*interceptParticipant,
	selectionKnown bool,
	allowAdds bool,
) *Intercept {
	stale := s.staleServiceParticipantKeysWithSelection(intercept, selected, selectionKnown)
	missing := false
	if allowAdds && selectionKnown {
		for key := range selected {
			if _, ok := intercept.participants[key]; !ok {
				missing = true
				break
			}
		}
	}
	if !missing && len(stale) == 0 {
		return intercept
	}
	return s.UpdateIntercept(interceptID, func(intercept *Intercept) {
		added := false
		published := intercept.publishedServiceParticipant()
		publishedKey := ""
		if published != nil {
			publishedKey = participantKey(published.namespace, published.kind, published.name)
		}
		if allowAdds && selectionKnown {
			for key, participant := range selected {
				if _, ok := intercept.participants[key]; !ok {
					intercept.participants[key] = participant
					added = true
				}
			}
		}
		prunedPublished := false
		for _, key := range s.staleServiceParticipantKeysWithSelection(intercept, selected, selectionKnown) {
			prunedPublished = prunedPublished || key == publishedKey
			delete(intercept.participants, key)
		}
		intercept.syncServiceWorkloads()
		if prunedPublished {
			intercept.reselectServiceReview()
		} else if added && intercept.Disposition == rpc.InterceptDispositionType_ACTIVE {
			intercept.Disposition = rpc.InterceptDispositionType_WAITING
			intercept.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s",
				strings.Join(intercept.pendingParticipants(), ", "))
		}
	})
}

func (is *Intercept) publishedServiceParticipant() *interceptParticipant {
	if is.PodName != "" {
		for _, participant := range is.participants {
			if participant.podName == is.PodName {
				return participant
			}
		}
	}
	if is.PodIp != "" {
		for _, participant := range is.participants {
			if participant.review != nil && participant.review.PodIp == is.PodIp {
				return participant
			}
		}
	}
	if is.Disposition == rpc.InterceptDispositionType_ACTIVE {
		return is.primaryParticipant()
	}
	return nil
}

func clearPublishedReview(intercept *Intercept) {
	intercept.PodIp = ""
	intercept.PodName = ""
	intercept.ApiPort = 0
	intercept.FtpPort = 0
	intercept.SftpPort = 0
	intercept.MountPoint = ""
	intercept.MechanismArgsDesc = ""
	intercept.Environment = nil
	intercept.Mounts = nil
}

func (is *Intercept) reselectServiceReview() {
	pending := is.pendingParticipants()
	if len(pending) > 0 {
		clearPublishedReview(is)
		is.Disposition = rpc.InterceptDispositionType_WAITING
		is.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s", strings.Join(pending, ", "))
		return
	}
	primary := is.primaryParticipant()
	if primary == nil || primary.review == nil {
		clearPublishedReview(is)
		is.Disposition = rpc.InterceptDispositionType_WAITING
		is.Message = "Waiting for primary workload Agent approval"
		return
	}
	applyReview(is, primary.review)
	is.PodName = primary.podName
	is.Disposition = rpc.InterceptDispositionType_ACTIVE
	is.Message = ""
}

func (is *Intercept) primaryParticipant() *interceptParticipant {
	var first *interceptParticipant
	for _, participant := range is.participants {
		if first == nil || participantKey(participant.namespace, participant.kind, participant.name) <
			participantKey(first.namespace, first.kind, first.name) {
			first = participant
		}
		if participant.namespace == is.Spec.Namespace && participant.name == is.Spec.Agent &&
			(is.Spec.WorkloadKind == "" || participant.kind == is.Spec.WorkloadKind) {
			return participant
		}
	}
	return first
}

func applyReview(intercept *Intercept, review *rpc.ReviewInterceptRequest) {
	intercept.Disposition = review.Disposition
	intercept.Message = review.Message
	intercept.PodIp = review.PodIp
	intercept.PodName = ""
	intercept.FtpPort = review.FtpPort
	intercept.SftpPort = review.SftpPort
	intercept.MountPoint = review.MountPoint
	intercept.MechanismArgsDesc = review.MechanismArgsDesc
	intercept.Environment = review.Environment
	intercept.Mounts = review.Mounts
}

func (is *Intercept) applyServiceReview(agent *rpc.AgentInfo, review *rpc.ReviewInterceptRequest) {
	participant := is.addParticipant(agent)
	if review.Disposition != rpc.InterceptDispositionType_ACTIVE {
		applyReview(is, review)
		if is.Message == "" {
			is.Message = fmt.Sprintf("Workload %q rejected the intercept", participant.name)
		}
		return
	}

	participant.review = proto.Clone(review).(*rpc.ReviewInterceptRequest)
	participant.podName = agent.PodName
	pending := is.pendingParticipants()
	if len(pending) > 0 {
		is.Disposition = rpc.InterceptDispositionType_WAITING
		is.Message = fmt.Sprintf("Waiting for Agent approval from workloads: %s", strings.Join(pending, ", "))
		return
	}

	primary := is.primaryParticipant()
	if primary == nil || primary.review == nil {
		is.Disposition = rpc.InterceptDispositionType_WAITING
		is.Message = "Waiting for primary workload Agent approval"
		return
	}
	applyReview(is, primary.review)
	is.PodName = primary.podName
	is.Disposition = rpc.InterceptDispositionType_ACTIVE
	is.Message = ""
}
