package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	core "k8s.io/api/core/v1"
	events "k8s.io/api/events/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/eventwatch"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/icept"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

// PrepareIntercept ensures that the given request can be matched against the intercept configuration of
// the workload that it references. It returns a PreparedIntercept where all intercepted ports have been
// qualified with a container port and if applicable, with service name and a service port name.
//
// The first step is to find the requested Workload and the agent config for that workload. This step will
// create the initial ConfigMap for the namespace if it doesn't exist yet, and also generate the actual
// intercept config if it doesn't exist.
//
// The second step matches all PortIdentifiers in the request to the intercepts of the agent config.
//
// It's expected that the client that makes the call will update any unqualified port identifiers
// with the ones in the returned PreparedIntercept.
func (s *State) PrepareIntercept(
	ctx context.Context,
	cr *rpc.CreateInterceptRequest,
	client *ClientSession,
) (pi *rpc.PreparedIntercept, err error) {
	interceptError := func(err error) (*rpc.PreparedIntercept, error) {
		clog.Errorf(ctx, "PrepareIntercept error %v", err)
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return &rpc.PreparedIntercept{Error: err.Error(), ErrorCategory: int32(errcat.GetCategory(err))}, nil
	}

	spec := cr.InterceptSpec
	kind := k8sapi.Kind(spec.WorkloadKind)
	enabledWorkloadKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	var wl k8sapi.Workload
	if kind == "" {
		for _, ek := range enabledWorkloadKinds {
			wl, err = agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, ek)
			if err == nil {
				break
			}
			if k8sErrors.IsNotFound(err) {
				continue
			}
			clog.Error(ctx, err)
			return interceptError(err)
		}
		if wl == nil {
			// unless there are zero enabled workload kinds, err must be set to a not-found error at this point
			return interceptError(errcat.User.New(k8sErrors.NewNotFound(core.Resource("workload"), spec.Agent+"."+spec.Namespace)))
		}
	} else {
		if !enabledWorkloadKinds.Contains(kind) {
			return interceptError(errcat.User.Newf("The %s kind is an not enabled workload kind", kind))
		}
		wl, err = agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, kind)
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				err = errcat.User.New(err)
			}
			clog.Error(ctx, err)
			return interceptError(err)
		}
	}

	if spec.NodeAgent {
		if err = nodeAgentGateErr(managerutil.GetEnv(ctx)); err != nil {
			return interceptError(err)
		}
		if spec.Replace {
			// Replace is implemented by the sidecar machinery, which
			// node-agent mode never runs, so the app container would keep
			// running and the replace would be silently ignored.
			return interceptError(errcat.User.New("node-agent mode does not support --replace"))
		}
	} else {
		// Provisioning a sidecar intercept injects a traffic-agent into the
		// workload's pod template and restarts its pods. A live node-agent
		// intercept depends on the specific pod (and its CRI container IDs)
		// that is currently running, so serving this request would break it
		// out from under its client. exceptID is irrelevant for a non-
		// node-agent spec (it can never equal a node-agent intercept's id);
		// it's passed because activeNodeAgentIntercept requires one.
		iid := fmt.Sprintf("%s:%s", client.id, spec.Name)
		if existing := s.activeNodeAgentIntercept(spec, iid); existing != nil {
			return interceptError(errcat.User.Newf(
				"%s.%s has a live node-agent intercept named %q created by client %q; "+
					"starting this intercept would inject a traffic-agent sidecar and restart the "+
					"workload's pods, breaking that node-agent intercept. Remove it first, or create "+
					"this intercept with --node-agent instead",
				spec.Agent, spec.Namespace, existing.Spec.Name, existing.Spec.Client))
		}
	}

	var rp agentconfig.ReplacePolicy
	if spec.Replace {
		rp = agentconfig.ReplacePolicyContainer
	} else {
		rp = agentconfig.ReplacePolicyIntercept
	}

	ac, _, err := s.ensureAgent(ctx, wl, s.isExtended(spec), true, spec, rp)
	if err != nil {
		return interceptError(err)
	}

	pi = &rpc.PreparedIntercept{
		Namespace:     ac.Namespace,
		AgentImage:    ac.AgentImage,
		WorkloadKind:  string(ac.WorkloadKind),
		ContainerName: spec.ContainerName,
		ServiceName:   spec.ServiceName,
	}

	var cn *agentconfig.Container
	if spec.NoDefaultPort {
		if cn, err = icept.FindContainer(ac, spec); err == nil {
			pi.ContainerName = cn.Name
			pi.ServiceName = ""
			if spec.PortIdentifier == "all" {
				prepareAllContainerPorts(cn, pi)
			} else if spec.PortIdentifier != "" {
				err = s.checkInterceptConsistency(ac, cn, cr, client, pi)
			}
		}
	} else {
		err = s.checkInterceptConsistency(ac, nil, cr, client, pi)
	}

	if err != nil {
		return interceptError(errcat.User.New(err))
	}
	pi.ServiceIps = serviceIPs(ctx, pi.ServiceName, ac.Namespace)
	lastActivity := time.Now()
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity %s", lastActivity)
	}

	if spec.NodeAgent {
		// ac was generated with dryRun=true above, so no sidecar was injected
		// and the workload's pods were not restarted. A node-agent Job is a
		// standalone pod, not a sidecar, so it's provisioned here instead of
		// via the injecting ensureAgent(dryRun=false) that AddIntercept would
		// otherwise call.
		if err = s.ensureNodeAgent(ctx, wl, ac, true); err != nil {
			return interceptError(err)
		}
	}
	return pi, nil
}

// activeNodeAgentIntercept returns a live node-agent intercept for the same
// agent and namespace as spec, other than the one identified by exceptID, or
// nil if there is none. spec's own NodeAgent flag is irrelevant: the search
// is keyed only by agent and namespace, so this also finds a node-agent
// intercept that a proposed sidecar intercept (spec.NodeAgent == false) would
// conflict with.
func (s *State) activeNodeAgentIntercept(spec *rpc.InterceptSpec, exceptID string) *Intercept {
	return s.findLiveNodeAgentIntercept(spec.Agent, spec.Namespace, exceptID)
}

// findLiveNodeAgentIntercept scans for a live (non-REMOVED, non-child)
// node-agent intercept for the given agent name and namespace, other than
// the one identified by exceptID (pass "" to not exclude any), or nil if
// there is none. A child (pod-port) intercept shares its parent's spec, so
// matching the parent is sufficient.
func (s *State) findLiveNodeAgentIntercept(name, namespace, exceptID string) (existing *Intercept) {
	s.intercepts.Range(func(id string, ic *Intercept) bool {
		if id == exceptID || !ic.Spec.GetNodeAgent() || IsChildIntercept(ic.Spec) {
			return true
		}
		if ic.Spec.GetAgent() != name || ic.Spec.GetNamespace() != namespace {
			return true
		}
		if ic.Disposition == rpc.InterceptDispositionType_REMOVED {
			return true
		}
		existing = ic
		return false
	})
	return existing
}

// serviceIPs returns the cluster IPs of the named service, each in netip.Addr
// binary form.
func serviceIPs(ctx context.Context, serviceName, namespace string) (ips [][]byte) {
	if serviceName == "" {
		return nil
	}
	f := informer.GetK8sFactory(ctx, namespace)
	if f == nil {
		clog.Debugf(ctx, "no informer factory for namespace %s", namespace)
		return nil
	}
	svc, err := f.Core().V1().Services().Lister().Services(namespace).Get(serviceName)
	if err != nil {
		clog.Debugf(ctx, "unable to get service %s.%s: %v", serviceName, namespace, err)
		return nil
	}
	sips := svc.Spec.ClusterIPs
	if len(sips) == 0 {
		sips = []string{svc.Spec.ClusterIP}
	}
	for _, is := range sips {
		// Headless services have the IP "None", which doesn't parse.
		if ip, err := netip.ParseAddr(is); err == nil {
			if b, err := ip.MarshalBinary(); err == nil {
				ips = append(ips, b)
			}
		}
	}
	return ips
}

func servicePodIsRelevant(pod *core.Pod) bool {
	// The shared pod informer deliberately strips Status.Conditions. Service
	// discovery still needs every selector-matching pod that has not started
	// deleting so it can detect pod-only labels and resolve current owners.
	return pod.DeletionTimestamp == nil
}

func sidecarClaimsService(sc *agentconfig.Sidecar, spec *rpc.InterceptSpec) bool {
	for _, container := range sc.Containers {
		for _, target := range container.Intercepts {
			if string(target.ServiceUID) != spec.ServiceUid || target.Protocol.String() != spec.Protocol {
				continue
			}
			if spec.ServicePort > 0 && int32(target.ServicePort) == spec.ServicePort {
				return true
			}
			if spec.ServicePort == 0 && spec.ServicePortName != "" && target.ServicePortName == spec.ServicePortName {
				return true
			}
		}
	}
	return false
}

func sortedServiceWorkloads(workloads map[string]k8sapi.Workload) []k8sapi.Workload {
	keys := make([]string, 0, len(workloads))
	for key := range workloads {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]k8sapi.Workload, len(keys))
	for i, key := range keys {
		result[i] = workloads[key]
	}
	return result
}

func addServiceWorkload(workloads map[string]k8sapi.Workload, workload k8sapi.Workload) {
	workloads[participantKey(workload.GetNamespace(), string(workload.GetKind()), workload.GetName())] = workload
}

var (
	errServicePodOnlySelection        = errors.New("service selects only individual pods from a workload")
	errServicePodOwnerUnrepresentable = errors.New("service selects a pod without a supported workload owner")
	errServiceIdentityChanged         = errors.New("service is missing or changed identity")
	errServiceSelectorless            = errors.New("service has no selector")
)

// discoverServiceWorkloads finds both selector-matching workload templates
// and current selected pod owners. The former keeps scaled-to-zero and
// starting workloads from escaping when they later become ready. A workload
// selected only by labels on one of its pods cannot be expanded safely,
// because agent configuration is shared by every pod in that workload.
func discoverServiceWorkloads(ctx context.Context, spec *rpc.InterceptSpec) ([]k8sapi.Workload, bool, error) {
	if !serviceScopedIntercept(spec) || spec.ServiceName == "" {
		return nil, false, nil
	}

	f := informer.GetFactory(ctx, spec.Namespace)
	if f == nil {
		return nil, false, nil
	}
	kf := f.GetK8sInformerFactory()
	svc, err := kf.Core().V1().Services().Lister().Services(spec.Namespace).Get(spec.ServiceName)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return nil, true, fmt.Errorf("%w: service %s.%s no longer exists",
				errServiceIdentityChanged, spec.ServiceName, spec.Namespace)
		}
		return nil, true, err
	}
	if string(svc.UID) != spec.ServiceUid {
		return nil, true, fmt.Errorf("%w: service %s.%s changed identity while preparing intercept",
			errServiceIdentityChanged, spec.ServiceName, spec.Namespace)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, true, fmt.Errorf("%w: service %s.%s has no selector",
			errServiceSelectorless, spec.ServiceName, spec.Namespace)
	}

	selector := labels.SelectorFromSet(svc.Spec.Selector)
	enabledKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	workloads := make(map[string]k8sapi.Workload)
	addTemplate := func(workload k8sapi.Workload) {
		if hasValidReplicasetOwner(workload, enabledKinds) {
			return
		}
		if workload.GetKind() == k8sapi.DeploymentKind &&
			agentmap.TrafficManagerSelector.Matches(labels.Set(workload.GetLabels())) {
			return
		}
		if selector.Matches(labels.Set(workload.GetPodTemplate().Labels)) {
			addServiceWorkload(workloads, workload)
		}
	}

	ai := kf.Apps().V1()
	for _, kind := range enabledKinds {
		switch kind {
		case k8sapi.DeploymentKind:
			deployments, err := ai.Deployments().Lister().Deployments(spec.Namespace).List(labels.Everything())
			if err != nil {
				return nil, true, err
			}
			for _, deployment := range deployments {
				addTemplate(k8sapi.Deployment(deployment))
			}
		case k8sapi.ReplicaSetKind:
			replicaSets, err := ai.ReplicaSets().Lister().ReplicaSets(spec.Namespace).List(labels.Everything())
			if err != nil {
				return nil, true, err
			}
			for _, replicaSet := range replicaSets {
				addTemplate(k8sapi.ReplicaSet(replicaSet))
			}
		case k8sapi.StatefulSetKind:
			statefulSets, err := ai.StatefulSets().Lister().StatefulSets(spec.Namespace).List(labels.Everything())
			if err != nil {
				return nil, true, err
			}
			for _, statefulSet := range statefulSets {
				addTemplate(k8sapi.StatefulSet(statefulSet))
			}
		case k8sapi.RolloutKind:
			ri := f.GetArgoRolloutsInformerFactory().Argoproj().V1alpha1().Rollouts()
			rollouts, err := ri.Lister().Rollouts(spec.Namespace).List(labels.Everything())
			if err != nil {
				return nil, true, err
			}
			for _, rollout := range rollouts {
				addTemplate(k8sapi.Rollout(rollout))
			}
		}
	}

	pods, err := kf.Core().V1().Pods().Lister().Pods(spec.Namespace).List(selector)
	if err != nil {
		return nil, true, err
	}
	for _, pod := range pods {
		if !servicePodIsRelevant(pod) {
			continue
		}
		workload, err := agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), enabledKinds)
		if err != nil {
			var ownerNotFound *agentmap.WorkloadOwnerNotFoundError
			if errors.As(err, &ownerNotFound) {
				return nil, true, fmt.Errorf("%w: service endpoint pod %s.%s has no supported workload owner: %v",
					errServicePodOwnerUnrepresentable, pod.Name, pod.Namespace, err)
			}
			return nil, true, fmt.Errorf("unable to resolve workload owner for service endpoint pod %s.%s: %w", pod.Name, pod.Namespace, err)
		}
		if !selector.Matches(labels.Set(workload.GetPodTemplate().Labels)) {
			return nil, true, fmt.Errorf("%w: service endpoint pod %s.%s belongs to %s whose pod template does not match the Service selector",
				errServicePodOnlySelection, pod.Name, pod.Namespace, workload)
		}
		addServiceWorkload(workloads, workload)
	}
	return sortedServiceWorkloads(workloads), true, nil
}

// serviceWorkloads keeps the explicitly requested workload when informer
// state is unavailable, preserving single-workload interception.
func serviceWorkloads(ctx context.Context, spec *rpc.InterceptSpec, primary k8sapi.Workload) ([]k8sapi.Workload, error) {
	workloads := map[string]k8sapi.Workload{
		participantKey(primary.GetNamespace(), string(primary.GetKind()), primary.GetName()): primary,
	}
	discovered, known, err := discoverServiceWorkloads(ctx, spec)
	if err != nil {
		return nil, err
	}
	if !known {
		return sortedServiceWorkloads(workloads), nil
	}
	for _, workload := range discovered {
		addServiceWorkload(workloads, workload)
	}
	return sortedServiceWorkloads(workloads), nil
}

// checkServiceWorkloadConflicts resolves the target container for a
// secondary workload without changing its stored agent config. This must run
// before shared-Service expansion changes ReplacePolicyContainer to
// ReplacePolicyIntercept and restarts pods.
func (s *State) checkServiceWorkloadConflicts(
	ctx context.Context,
	spec *rpc.InterceptSpec,
	workload k8sapi.Workload,
	client *ClientSession,
) error {
	return s.checkServiceWorkloadConflictsIgnoring(ctx, spec, workload, client, "")
}

func (s *State) checkServiceWorkloadConflictsIgnoring(
	ctx context.Context,
	spec *rpc.InterceptSpec,
	workload k8sapi.Workload,
	client *ClientSession,
	ignoredInterceptID string,
) error {
	if client == nil {
		return fmt.Errorf("client session is unavailable")
	}
	agentImage := managerutil.GetAgentImage(ctx)
	if err := s.ValidateAgentImage(agentImage, s.isExtended(spec)); err != nil {
		return err
	}
	var config *agentconfig.Sidecar
	if mm := mutator.GetMap(ctx); mm != nil {
		config = mm.Get(workload.GetName(), workload.GetNamespace())
	}
	if config == nil || !sidecarClaimsService(config, spec) {
		var err error
		config, err = s.createAgentConfig(ctx, workload, agentImage)
		if err != nil {
			return err
		}
	}
	secondarySpec := proto.Clone(spec).(*rpc.InterceptSpec)
	// Different selected workloads can use different container names for the
	// same Service target. Resolve the secondary workload's own container.
	secondarySpec.ContainerName = ""
	_, ic, err := config.FindIntercept(
		secondarySpec.ServiceName,
		secondarySpec.ContainerName,
		types.PortIdentifier(secondarySpec.PortIdentifier),
	)
	if err != nil {
		return err
	}
	_, containerPorts, err := checkPortConsistency(config, nil, ic, secondarySpec)
	if err != nil {
		return err
	}
	conflictSpec := proto.Clone(secondarySpec).(*rpc.InterceptSpec)
	conflictSpec.ServiceUid = string(ic.ServiceUID)
	conflictSpec.ServicePortName = ic.ServicePortName
	conflictSpec.ServicePort = int32(ic.ServicePort)
	conflictSpec.Protocol = ic.Protocol.String()
	conflictSpec.ContainerPort = int32(ic.ContainerPort)
	return s.checkInterceptConflictsIgnoring(config, client, containerPorts, conflictSpec, ignoredInterceptID)
}

func serviceWorkloadInfos(workloads []k8sapi.Workload) []*rpc.InterceptWorkload {
	infos := make([]*rpc.InterceptWorkload, len(workloads))
	for i, workload := range workloads {
		infos[i] = &rpc.InterceptWorkload{
			Namespace:    workload.GetNamespace(),
			WorkloadKind: string(workload.GetKind()),
			WorkloadName: workload.GetName(),
		}
	}
	return infos
}

func fallbackToSingleWorkload(ctx context.Context, spec *rpc.InterceptSpec, reason string) {
	clog.Warnf(ctx, "Shared-Service expansion for %q is unavailable: %s; using only requested workload %q",
		spec.ServiceName, reason, spec.Agent)
	spec.ServiceUid = ""
	spec.ServicePortName = ""
	spec.ServicePort = 0
}

func (s *State) ensureServiceWorkloads(
	ctx context.Context,
	spec *rpc.InterceptSpec,
	primary k8sapi.Workload,
	primaryConfig *agentconfig.Sidecar,
	primaryAgents []*AgentSession,
	rp agentconfig.ReplacePolicy,
	client *ClientSession,
) []*rpc.InterceptWorkload {
	if !serviceScopedIntercept(spec) {
		return nil
	}
	if spec.Replace || spec.Mechanism != "http" {
		fallbackToSingleWorkload(ctx, spec, "shared interception requires the http mechanism without --replace")
		return nil
	}

	workloads, err := serviceWorkloads(ctx, spec, primary)
	if err != nil {
		fallbackToSingleWorkload(ctx, spec, err.Error())
		return nil
	}
	if len(workloads) <= 1 {
		return serviceWorkloadInfos(workloads)
	}

	configWorkloads := make(map[string]k8sapi.Workload, len(workloads))
	for _, workload := range workloads {
		if workload.GetName() == primary.GetName() && workload.GetKind() == primary.GetKind() {
			continue
		}
		if err = s.checkServiceWorkloadConflicts(ctx, spec, workload, client); err != nil {
			fallbackToSingleWorkload(ctx, spec,
				fmt.Sprintf("workload %s conflicts with an existing intercept: %v", workload, err))
			return nil
		}
		configWorkloads[participantKey(workload.GetNamespace(), string(workload.GetKind()), workload.GetName())] = workload
	}

	for _, workload := range workloads {
		config := primaryConfig
		agents := primaryAgents
		if workload.GetName() != primary.GetName() || workload.GetKind() != primary.GetKind() {
			configWorkload := configWorkloads[participantKey(workload.GetNamespace(), string(workload.GetKind()), workload.GetName())]
			if k8sapi.ReadyReplicas(workload) == 0 {
				config, err = s.getOrCreateAgentConfig(ctx, configWorkload, s.isExtended(spec), false, spec, rp)
				if err == nil {
					err = mutator.GetMap(ctx).EvictPodsWithAgentConfigMismatch(ctx, workload, config)
				}
				agents = nil
			} else {
				config, agents, err = s.ensureAgent(ctx, configWorkload, s.isExtended(spec), false, spec, rp)
			}
			if err != nil {
				fallbackToSingleWorkload(ctx, spec,
					fmt.Sprintf("unable to ensure agent for workload %s: %v", workload, err))
				return nil
			}
		}
		if !sidecarClaimsService(config, spec) {
			fallbackToSingleWorkload(ctx, spec,
				fmt.Sprintf("agent config for workload %s does not claim the selected Service target", workload))
			return nil
		}
		currentAgents := s.LoadMatchingAgents(func(_ tunnel.SessionID, agent *AgentSession) bool {
			return agent.Namespace == workload.GetNamespace() &&
				agent.Kind == string(workload.GetKind()) &&
				agent.Name == workload.GetName()
		})
		if len(currentAgents) > 0 {
			agents = agents[:0]
			for _, agent := range currentAgents {
				agents = append(agents, agent)
			}
			sortAgents(agents)
		}
		if len(agents) == 0 && k8sapi.ReadyReplicas(workload) == 0 {
			continue
		}
		allClaim := len(agents) > 0
		for _, agent := range agents {
			if !agentClaimsService(agent.AgentInfo, spec) {
				allClaim = false
				break
			}
		}
		if !allClaim {
			fallbackToSingleWorkload(ctx, spec,
				fmt.Sprintf("not every agent for workload %s advertises the selected Service target", workload))
			return nil
		}
	}
	return serviceWorkloadInfos(workloads)
}

func prepareAllContainerPorts(cn *agentconfig.Container, pi *rpc.PreparedIntercept) {
	pics := agentconfig.PortUniqueIntercepts(cn)
	if ni := len(pics); ni > 0 {
		// Put the first port in the intercept itself
		i0 := pics[0]
		pi.ContainerPort = int32(i0.ContainerPort)
		pi.Protocol = i0.Protocol.String()
		if ni > 1 {
			// Put the remaining ports in PodPorts with a 1:1 mapping to target port on the client.
			pi.PodPorts = make([]string, ni-1)
			for i := 1; i < ni; i++ {
				ic := pics[i]
				pi.PodPorts[i-1] = fmt.Sprintf("%d:%d/%s", ic.ContainerPort, ic.ContainerPort, ic.Protocol)
			}
		}
	}
}

func (s *State) checkInterceptConsistency(
	ac *agentconfig.Sidecar,
	cn *agentconfig.Container,
	cr *rpc.CreateInterceptRequest,
	client *ClientSession,
	pi *rpc.PreparedIntercept,
) (err error) {
	spec := cr.InterceptSpec
	env := managerutil.GetEnv(s.backgroundCtx)

	// Check if global intercepts are allowed before proceeding
	// Block replaces and global TCP/UDP intercepts, but allow HTTP intercepts and wiretaps
	if !env.InterceptAllowGlobal && (spec.Replace || !(spec.Wiretap || spec.Mechanism == "http")) {
		return fmt.Errorf("global TCP/UDP intercepts and replaces are disabled. Use --http-header or --http-path-* flags for HTTP intercepts")
	}

	portID := types.PortIdentifier(spec.PortIdentifier)
	containerOnly := cn != nil

	var ic *agentconfig.Intercept
	if containerOnly {
		ic, err = icept.FindContainerIntercept(ac, cn, portID)
	} else {
		cn, ic, err = ac.FindIntercept(pi.ServiceName, pi.ContainerName, portID)
	}
	if err != nil {
		return err
	}
	pi.ContainerName = cn.Name
	if !containerOnly {
		cn = nil
	}
	podPorts, containerPorts, err := checkPortConsistency(ac, cn, ic, spec)
	if err != nil {
		return err
	}
	conflictSpec := proto.Clone(spec).(*rpc.InterceptSpec)
	conflictSpec.ServiceUid = string(ic.ServiceUID)
	conflictSpec.ServicePortName = ic.ServicePortName
	conflictSpec.ServicePort = int32(ic.ServicePort)
	conflictSpec.Protocol = ic.Protocol.String()
	conflictSpec.ContainerPort = int32(ic.ContainerPort)
	err = s.checkInterceptConflicts(ac, client, containerPorts, conflictSpec)
	if err != nil {
		return err
	}
	pi.ServiceUid = string(ic.ServiceUID)
	pi.ServiceName = ic.ServiceName
	pi.ServicePortName = ic.ServicePortName
	pi.Protocol = ic.Protocol.String()
	pi.ContainerPort = int32(ic.ContainerPort)
	pi.ServicePort = int32(ic.ServicePort)
	pi.PodPorts = podPorts
	return nil
}

func checkPortConsistency(
	ac *agentconfig.Sidecar,
	cn *agentconfig.Container,
	ic *agentconfig.Intercept,
	spec *rpc.InterceptSpec,
) ([]string, []types.PortAndProto, error) {
	cp := types.PortAndProto{Proto: ic.Protocol, Port: ic.ContainerPort}
	if len(spec.PodPorts) == 0 {
		return nil, []types.PortAndProto{cp}, nil
	}

	containerPorts := make(map[types.PortAndProto]struct{})
	containerPorts[types.PortAndProto{Proto: ic.Protocol, Port: ic.ContainerPort}] = struct{}{}

	podPorts := make([]string, len(spec.PodPorts))
	uniqueTargets := make(map[types.PortAndProto]struct{})
	uniqueTargets[types.PortAndProto{Proto: ic.Protocol, Port: uint16(spec.TargetPort)}] = struct{}{}
	for i, pms := range spec.PodPorts {
		pm := types.PortMapping(pms)
		var pmIc *agentconfig.Intercept
		var err error
		if cn != nil {
			pmIc, err = icept.FindContainerIntercept(ac, cn, pm.From())
		} else {
			_, pmIc, err = ac.FindIntercept(spec.ServiceName, spec.ContainerName, pm.From())
		}
		if err != nil {
			return nil, nil, err
		}

		to := pm.ToAsNumeric()
		if _, ok := uniqueTargets[to]; ok {
			return nil, nil, fmt.Errorf("multiple port definitions targeting %s", &to)
		}
		uniqueTargets[to] = struct{}{}

		from := types.PortAndProto{Proto: pmIc.Protocol, Port: pmIc.ContainerPort}
		if _, ok := containerPorts[from]; ok {
			return nil, nil, fmt.Errorf("multiple port definitions using container port %s", &from)
		}
		containerPorts[from] = struct{}{}

		// Return the resolved numeric container port.
		podPorts[i] = fmt.Sprintf("%d:%s", pmIc.ContainerPort, &to)
	}
	return podPorts, maps.KeySlice(containerPorts), nil
}

func (s *State) checkInterceptConflicts(ac *agentconfig.Sidecar, client *ClientSession, containerPorts []types.PortAndProto, spec *rpc.InterceptSpec) error {
	return s.checkInterceptConflictsIgnoring(ac, client, containerPorts, spec, "")
}

// serviceParticipantPotentialConflict reports whether an existing shared
// Service intercept occupies one of the requested ports in this sidecar's
// workload. The logical intercept stores the primary workload's resolved
// container port, but a secondary workload can route the same Service port to
// a different container port.
func serviceParticipantPotentialConflict(
	ac *agentconfig.Sidecar,
	containerPorts []types.PortAndProto,
	intercept *Intercept,
) bool {
	if !serviceScopedIntercept(intercept.Spec) || intercept.Spec.Wiretap {
		return false
	}
	switch intercept.Disposition {
	case rpc.InterceptDispositionType_ACTIVE, rpc.InterceptDispositionType_WAITING, rpc.InterceptDispositionType_NO_AGENT:
	default:
		return false
	}

	workloadName := ac.WorkloadName
	if workloadName == "" {
		workloadName = ac.AgentName
	}
	workloadKind := string(ac.WorkloadKind)
	matchesWorkload := func(namespace, kind, name string) bool {
		return namespace == ac.Namespace && name == workloadName &&
			(workloadKind == "" || kind == "" || kind == workloadKind)
	}
	participant := false
	if len(intercept.participants) > 0 {
		for _, candidate := range intercept.participants {
			if matchesWorkload(candidate.namespace, candidate.kind, candidate.name) {
				participant = true
				break
			}
		}
	} else {
		for _, candidate := range intercept.ServiceWorkloads {
			if candidate != nil && matchesWorkload(candidate.Namespace, candidate.WorkloadKind, candidate.WorkloadName) {
				participant = true
				break
			}
		}
	}
	if !participant {
		return false
	}

	portName := intercept.Spec.ServicePortName
	if portName == "" && intercept.Spec.ServicePort > 0 {
		portName = fmt.Sprintf("%d", intercept.Spec.ServicePort)
	}
	portID, err := types.NewPortIdentifier(
		types.FromK8sProtocol(core.Protocol(intercept.Spec.Protocol)),
		portName,
	)
	if err != nil {
		return false
	}
	_, target, err := ac.FindIntercept(intercept.Spec.ServiceName, "", portID)
	if err != nil {
		return false
	}
	if !servicePortMatches(&rpc.AgentInfo_InterceptTarget{
		ServiceUid:      string(target.ServiceUID),
		ServicePortName: target.ServicePortName,
		ServicePort:     int32(target.ServicePort),
		Protocol:        target.Protocol.String(),
	}, intercept.Spec) {
		return false
	}
	targetPort := types.PortAndProto{Proto: target.Protocol, Port: target.ContainerPort}
	for _, containerPort := range containerPorts {
		if containerPort == targetPort {
			return true
		}
	}
	return false
}

func (s *State) checkInterceptConflictsIgnoring(
	ac *agentconfig.Sidecar,
	client *ClientSession,
	containerPorts []types.PortAndProto,
	spec *rpc.InterceptSpec,
	ignoredInterceptID string,
) error {
	if spec.Wiretap {
		// A wiretap intercept is never in conflict with any other intercept.
		return nil
	}

	// Validate that there's no port conflict with other intercepts using the same agent.
	potentialConflicts := s.intercepts.LoadMatching(func(id string, info *Intercept) bool {
		if id == ignoredInterceptID {
			return false
		}
		if serviceScopesOverlap(spec, info.Spec) {
			switch info.Disposition {
			case rpc.InterceptDispositionType_ACTIVE, rpc.InterceptDispositionType_WAITING, rpc.InterceptDispositionType_NO_AGENT:
				return !info.Spec.Wiretap
			}
		}
		if serviceParticipantPotentialConflict(ac, containerPorts, info) {
			return true
		}
		return icept.PotentialConflict(ac.AgentName, ac.Namespace, containerPorts, info.InterceptInfo)
	})
	if len(potentialConflicts) == 0 {
		return nil
	}

	var overrides map[string]string
	for _, otherIc := range potentialConflicts {
		oSpec := otherIc.Spec
		if icept.IsInConflict(spec, oSpec) {
			var port string
			switch {
			case oSpec.ServiceUid == "":
				port = fmt.Sprintf("container %s, port %d", oSpec.ContainerName, oSpec.ContainerPort)
			case oSpec.ServicePort > 0:
				port = fmt.Sprintf("port %d", oSpec.ServicePort)
			default:
				port = fmt.Sprintf("port %q", oSpec.ServicePortName)
			}
			otherClient := s.GetClient(tunnel.SessionID(otherIc.ClientSession.SessionId))
			explain := icept.ExplainConflict(spec, oSpec)
			if otherClient == nil || time.Since(otherClient.lastMarked()) > managerutil.GetEnv(s.backgroundCtx).InterceptInactiveBlockTimeout {
				if overrides == nil {
					overrides = make(map[string]string)
				}
				overrides[otherIc.Id] = fmt.Sprintf("conflict with intercept %s:%s on %s created by client %q: %s", client.id, spec.Name, port, spec.Client, explain)
				continue
			}
			return fmt.Errorf("conflict with intercept %s on %s created by client %q: %s", otherIc.Id, port, oSpec.Client, explain)
		}
	}
	for id, msg := range overrides {
		// Intercept is in conflict and the client is inactive, so mark it for removal.
		s.UpdateIntercept(id, func(intercept *Intercept) {
			intercept.Disposition = rpc.InterceptDispositionType_AGENT_ERROR
			intercept.Message = msg
		})
	}
	return nil
}

func serviceScopesOverlap(a, b *rpc.InterceptSpec) bool {
	if !serviceScopedIntercept(a) || !serviceScopedIntercept(b) || a.ServiceUid != b.ServiceUid || a.Protocol != b.Protocol {
		return false
	}
	if a.ServicePort > 0 && b.ServicePort > 0 {
		return a.ServicePort == b.ServicePort
	}
	return a.ServicePortName != "" && a.ServicePortName == b.ServicePortName
}

func (s *State) AddIntercept(ctx context.Context, cir *rpc.CreateInterceptRequest) (*ClientSession, *rpc.InterceptInfo, error) {
	clientSession := cir.Session
	sessionID := tunnel.SessionID(clientSession.SessionId)
	client := s.GetClient(sessionID)
	if client == nil {
		return nil, nil, grpcErrors.Errorf(codes.NotFound, "session %q not found", sessionID)
	}

	spec := cir.InterceptSpec
	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)

	wl, err := agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, k8sapi.Kind(spec.WorkloadKind))
	if err != nil {
		code := codes.Internal
		if k8sErrors.IsNotFound(err) {
			code = codes.NotFound
		}
		return nil, nil, grpcErrors.Error(code, err.Error())
	}

	rp := agentconfig.ReplacePolicyIntercept
	if spec.Replace {
		rp = agentconfig.ReplacePolicyContainer
	}
	var primaryConfig *agentconfig.Sidecar
	var primaryAgents []*AgentSession
	var serviceWorkloads []*rpc.InterceptWorkload
	if spec.NodeAgent {
		// PrepareIntercept already provisioned the node-agent Job. Running
		// the injecting ensureAgent here would inject a sidecar and restart
		// the target pod, invalidating the container ID the Job was given.
		// Wait for its agent to register instead. The agent's name and
		// namespace always equal the workload's (see agentmap's config
		// generator), so no agent config is needed for the wait.
		if _, err = s.waitForNodeAgent(ctx, wl.GetName(), wl.GetNamespace()); err != nil {
			return nil, nil, err
		}
		if serviceScopedIntercept(spec) {
			// Node-agent intercepts target the pod selected during
			// PrepareIntercept. They cannot provision the other workloads
			// behind a shared Service, so don't let existing sidecars make
			// this intercept appear partially service-scoped.
			fallbackToSingleWorkload(ctx, spec, "node-agent intercepts target only the requested workload")
		}
	} else {
		primaryConfig, primaryAgents, err = s.ensureAgent(ctx, wl, s.isExtended(spec), false, spec, rp)
		if err != nil {
			return nil, nil, err
		}
		serviceWorkloads = s.ensureServiceWorkloads(ctx, spec, wl, primaryConfig, primaryAgents, rp, client)
	}

	is, err := s.addIntercept(interceptID, cir, serviceWorkloads)
	if err != nil {
		return nil, nil, err
	}

	// Add one child intercept for each pod-port.
	for _, pms := range spec.PodPorts {
		pm := types.PortMapping(pms)
		from, to, err := pm.FromNumberAndTo()
		if err != nil {
			// Did PrepareIntercept create an invalid pod_port?
			return nil, nil, grpcErrors.Errorf(codes.Internal, "invalid pod_port %q: %v", pm, err)
		}
		pmCir := proto.Clone(cir).(*rpc.CreateInterceptRequest)
		pmSpec := pmCir.InterceptSpec
		pmSpec.Name = fmt.Sprintf("%s-%d-%s", spec.Name, from, strings.ToLower(string(to.Proto)))
		pmSpec.PodPorts = nil
		pmSpec.LocalPorts = nil

		// This intercept targets a pod-port (container port) directly. A container name
		// is not necessary because container ports must be unique within the pod.
		pmSpec.ServiceUid = ""
		pmSpec.ServicePortName = ""
		pmSpec.ServicePort = 0
		pmSpec.Protocol = string(to.Proto)
		pmSpec.ContainerPort = int32(from)
		pmSpec.PortIdentifier = pm.From().String()
		pmSpec.TargetPort = int32(pm.ToAsNumeric().Port)

		// The Client field helps IsChildIntercept identify the child.
		pmSpec.Client = fmt.Sprintf("child %s %s %s", pm, spec.Name, spec.Client)

		pmInterceptID := fmt.Sprintf("%s:%s", sessionID, pmSpec.Name)
		_, err = s.addIntercept(pmInterceptID, pmCir, nil)
		if err != nil {
			return nil, nil, err
		}

		// Add finalizer to the interceptState of the parent intercept.
		is.addFinalizer(func(_ context.Context, _ *rpc.InterceptInfo) error {
			s.intercepts.LoadAndDelete(pmInterceptID)
			return nil
		})
	}
	if !spec.NodeAgent {
		// A node-agent intercept never injects a sidecar or modifies the
		// workload's pod template, so there is nothing for this finalizer
		// to restore.
		err = s.AddInterceptFinalizer(interceptID, func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
			return s.restoreAppContainer(ctx, interceptInfo, wl)
		})
		if err != nil {
			clog.Errorf(ctx, "Failed to add finalizer for %s: %v", interceptID, err)
		}
	}
	if spec.NodeAgent {
		if err = s.AddInterceptFinalizer(interceptID, s.nodeAgentReapFinalizer()); err != nil {
			clog.Errorf(ctx, "Failed to add node-agent reap finalizer for %s: %v", interceptID, err)
		}
		// Started here rather than in PrepareIntercept, because a watcher
		// checks nodeAgentWanted as soon as it starts, and the intercept it
		// would claim on behalf of does not exist until addIntercept above
		// has stored it.
		s.startNodeAgentPodWatch(spec.Agent, spec.Namespace)
	}
	if serviceScopedIntercept(spec) {
		s.startServiceInterceptWatch(interceptID)
	}

	agentType := "sidecar"
	if spec.NodeAgent {
		agentType = "node"
	}
	usg.Quick(ctx, "manager.attach", "agent.type", agentType, "mechanism", spec.Mechanism)
	return client, is.InterceptInfo, nil
}

func IsChildIntercept(spec *rpc.InterceptSpec) bool {
	return strings.HasPrefix(spec.Client, "child ")
}

func (s *State) GetParentIntercept(sessionID tunnel.SessionID, spec *rpc.InterceptSpec) (*Intercept, bool) {
	childCols := strings.Split(spec.Client, " ")
	if len(childCols) != 4 {
		return nil, false
	}
	return s.intercepts.Load(fmt.Sprintf("%s:%s", sessionID, childCols[2]))
}

func (s *State) addIntercept(id string, cir *rpc.CreateInterceptRequest, serviceWorkloads []*rpc.InterceptWorkload) (*Intercept, error) {
	is := s.NewInterceptInfo(id, cir)
	is.ServiceWorkloads = serviceWorkloads
	s.initializeParticipants(is)

	// Wrap each potential-state-change in an
	//
	//     if cept.Disposition == rpc.InterceptDispositionType_WAITING { … }
	//
	// so that we don't need to worry about different state-changes stomping on each-other.
	if is.Disposition == rpc.InterceptDispositionType_WAITING {
		if errCode, errMsg := s.checkAgentsForIntercept(is); errCode != 0 {
			is.Disposition = errCode
			is.Message = errMsg
		}
	}

	if existingValue, hasConflict := s.intercepts.LoadOrStore(id, is); hasConflict {
		if existingValue.Disposition != rpc.InterceptDispositionType_REMOVED {
			return nil, grpcErrors.Errorf(codes.AlreadyExists, "Intercept named %q already exists", is.Spec.Name)
		}
		s.intercepts.Store(id, is)
	}
	return is, nil
}

func (s *State) NewInterceptInfo(interceptID string, ciReq *rpc.CreateInterceptRequest) *Intercept {
	return &Intercept{
		InterceptInfo: &rpc.InterceptInfo{
			Spec:          ciReq.InterceptSpec,
			Disposition:   rpc.InterceptDispositionType_WAITING,
			Message:       "Waiting for Agent approval",
			Id:            interceptID,
			ClientSession: ciReq.Session,
			ModifiedAt:    timestamppb.Now(),
		},
	}
}

func (s *State) AddInterceptFinalizer(interceptID string, finalizer InterceptFinalizer) error {
	is, ok := s.intercepts.Load(interceptID)
	if !ok {
		return grpcErrors.Errorf(codes.NotFound, "no such intercept %s", interceptID)
	}
	is.addFinalizer(finalizer)
	return nil
}

// EnsureAgent ensures that an agent exists for the workload named n in
// namespace ns and waits for it to become available. When nodeAgent is
// requested, it provisions (or reuses) a node-hosted traffic-agent Job
// instead of injecting a sidecar, and takes a lease on it under sessionID so
// that the Job outlives this call for as long as the session does, until
// ReleaseAgent is called or the session ends. A sidecar request is rejected
// while a node-agent intercept or lease already claims the workload, since
// injecting a sidecar would restart the pod the node-agent depends on.
func (s *State) EnsureAgent(ctx context.Context, sessionID tunnel.SessionID, n, ns string, nodeAgent bool) (as []*AgentSession, err error) {
	if !nodeAgent && s.nodeAgentWanted(n, ns) {
		// Checked before resolving the workload: a sidecar request against a
		// workload a node-agent already claims is rejected outright, so
		// there is no reason to pay for a workload lookup first.
		return nil, errcat.User.Newf(
			"%s.%s has a live node-agent intercept or ingest claim; injecting a traffic-agent "+
				"sidecar would restart the pods the node-agent depends on. Use --node-agent, or "+
				"release the existing claim first",
			n, ns)
	}

	var wl k8sapi.Workload
	wl, err = agentmap.GetWorkload(ctx, n, ns, "")
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			err = errcat.User.New(err)
		}
		return nil, err
	}

	if nodeAgent {
		if err = nodeAgentGateErr(managerutil.GetEnv(ctx)); err != nil {
			return nil, err
		}
		var sc *agentconfig.Sidecar
		sc, err = s.getOrCreateAgentConfig(ctx, wl, false, true, nil, agentconfig.ReplacePolicyInactive)
		if err != nil {
			return nil, err
		}
		if err = s.ensureNodeAgent(ctx, wl, sc, false); err != nil {
			return nil, err
		}
		// The lease is taken before waiting, not after: the reconciler's
		// orphan grace period (nodeAgentOrphanGracePeriod) only protects a
		// freshly created Job from the periodic sweep, and only for a
		// limited time. Taking the lease immediately closes the window
		// against every reap path -- the sweep, a concurrent intercept's
		// removal finalizer, a concurrent ReleaseAgent, this session ending
		// -- for as long as the wait below takes, not just the sweep's
		// grace period.
		s.addLease(sessionID, n, ns)
		s.startNodeAgentPodWatch(n, ns)
		as, err = s.waitForNodeAgent(ctx, n, ns)
		if err != nil {
			s.removeLease(sessionID, n, ns)
			return nil, err
		}
		usg.Quick(ctx, "manager.attach", "agent.type", "node")
		return as, nil
	}

	_, as, err = s.ensureAgent(ctx, wl, false, false, nil, agentconfig.ReplacePolicyInactive)
	if err == nil {
		usg.Quick(ctx, "manager.attach", "agent.type", "sidecar")
	}
	return as, err
}

func (s *State) ValidateCreateAgent(context.Context, k8sapi.Workload, *agentconfig.Sidecar) error {
	return nil
}

// sortAgents will sort the given AgentInfo based on pod name.
func sortAgents(as []*AgentSession) {
	sort.Slice(as, func(i, j int) bool {
		return as[i].PodName < as[j].PodName
	})
}

func (s *State) ensureAgent(parentCtx context.Context, wl k8sapi.Workload, extended, dryRun bool, spec *rpc.InterceptSpec, rp agentconfig.ReplacePolicy) (
	*agentconfig.Sidecar, []*AgentSession, error,
) {
	if agentmap.TrafficManagerSelector.Matches(labels.Set(wl.GetLabels())) {
		msg := fmt.Sprintf("%s is the Telepresence Traffic Manager. It can not have a traffic-agent", wl)
		clog.Error(parentCtx, msg)
		return nil, nil, status.Error(codes.FailedPrecondition, msg)
	}

	// A node-agent request needs only the generated config, never
	// injection: the webhook and the manual-annotation path below are both
	// sidecar concepts that a node-agent spec must bypass entirely.
	if !managerutil.AgentInjectorEnabled(parentCtx) && !spec.GetNodeAgent() {
		cfgJSON, ok := wl.GetPodTemplate().Annotations[annotation.Config]
		if !ok {
			msg := fmt.Sprintf("agent-injector is disabled and no agent has been added manually for %s", wl)
			return nil, nil, status.Error(codes.FailedPrecondition, msg)
		}
		sc, err := agentconfig.UnmarshalJSON(cfgJSON)
		if err != nil {
			return nil, nil, err
		}
		am := s.LoadMatchingAgents(func(_ tunnel.SessionID, ai *AgentSession) bool {
			return ai.Name == sc.AgentName && ai.Namespace == sc.Namespace
		})
		as := make([]*AgentSession, len(am))
		i := 0
		for _, found := range am {
			as[i] = found
			i++
		}
		sortAgents(as)
		return sc, as, nil
	}

	if dryRun {
		sc, err := s.getOrCreateAgentConfig(parentCtx, wl, extended, dryRun, spec, rp)
		if err != nil {
			return nil, nil, err
		}
		return sc, nil, nil
	}

	ctx, cancel := context.WithTimeout(parentCtx, managerutil.GetEnv(parentCtx).AgentArrivalTimeout)
	defer cancel()

	failedCreateCh, err := eventwatch.WatchWarnings(ctx, k8sapi.GetK8sInterface(ctx), wl.GetNamespace(), wl.GetName())
	if err != nil {
		return nil, nil, err
	}

	sc, err := s.getOrCreateAgentConfig(ctx, wl, extended, dryRun, spec, rp)
	if err != nil {
		return nil, nil, err
	}
	err = mutator.GetMap(ctx).EvictPodsWithAgentConfigMismatch(ctx, wl, sc)
	if err != nil {
		clog.Errorf(ctx, "failed to inactivate pods: %v", err)
		return nil, nil, err
	}
	// The injector puts exactly one traffic-agent in each pod it just
	// evicted, so this wait always expects a single arrival.
	as, err := s.waitForAgents(ctx, sc.AgentName, sc.Namespace, false, 1, failedCreateCh)
	if err != nil {
		// If no agent arrives, then drop its entry from the configmap. This ensures that there
		// are no false positives the next time an intercept is attempted.
		s.dropAgentConfig(parentCtx, wl)
		return nil, nil, err
	}
	sortAgents(as)
	return sc, as, nil
}

// waitForNodeAgent waits for the node-agent Job(s) for the agent named name
// in namespace (created during PrepareIntercept, or by ensureNodeAgent for
// an ingest EnsureAgent call) to register themselves as agents, without
// injecting a sidecar. It mirrors the wait ensureAgent performs for the
// sidecar path, but skips config persistence and pod eviction, since a
// node-agent is a standalone Job rather than an injected container. The
// caller is responsible for having already provisioned the Job(s).
func (s *State) waitForNodeAgent(parentCtx context.Context, name, namespace string) ([]*AgentSession, error) {
	ctx, cancel := context.WithTimeout(parentCtx, managerutil.GetEnv(parentCtx).AgentArrivalTimeout)
	defer cancel()

	// The node-agent Job may already have been created (e.g. during
	// PrepareIntercept) before this watch starts, so an early failure event
	// can be missed. The controllers involved (scheduler, kubelet) retry and
	// re-emit warnings on each attempt, and the ctx timeout diagnosis in
	// waitForAgents covers whatever this watch doesn't catch.
	env := managerutil.GetEnv(parentCtx)
	failedCreateCh, err := eventwatch.WatchWarnings(ctx, k8sapi.GetK8sInterface(ctx), env.ManagerNamespace, nodeAgentJobNamePrefix(name))
	if err != nil {
		return nil, err
	}

	// The Job set was just ensured by the caller; counting it, rather than
	// re-listing the workload's pods, keeps this wait consistent with what
	// was really created. A workload with no matching Job yet (a race with
	// the caller's own creation) falls back to waiting for a single agent.
	jobTargets, err := nodeAgentJobsForWorkload(ctx, name, namespace)
	if err != nil {
		return nil, err
	}
	expected := len(jobTargets)
	if expected == 0 {
		expected = 1
	}

	as, err := s.waitForAgents(ctx, name, namespace, true, expected, failedCreateCh)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		if missing := missingNodeAgentTargets(jobTargets, as); len(missing) > 0 {
			err = errcat.User.Newf("%s\nNo agent registered for target pod(s): %s", err.Error(), strings.Join(missing, ", "))
		}
	}
	return as, err
}

// missingNodeAgentTargets returns the podName of every jobTargets entry
// whose Job has no corresponding session in arrived. A node-agent Job's own
// pod name is generated with the Job's name as prefix, so an arrived agent
// is matched back to its Job by that prefix rather than by the target pod
// it carries (which names the workload's pod, not the node-agent's own).
func missingNodeAgentTargets(jobTargets []nodeAgentJobTarget, arrived []*AgentSession) []string {
	var missing []string
	for _, jt := range jobTargets {
		found := false
		for _, a := range arrived {
			if strings.HasPrefix(a.PodName, jt.jobName+"-") {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, jt.podName)
		}
	}
	return missing
}

func (s *State) isExtended(spec *rpc.InterceptSpec) bool {
	return spec.Mechanism != "tcp" && spec.Mechanism != "http"
}

func (s *State) ValidateAgentImage(agentImage string, extended bool) (err error) {
	if agentImage == "" {
		err = errcat.User.Newf(
			"intercepts are disabled because the traffic-manager is unable to determine what image to use for injected traffic-agents.")
	} else if extended {
		err = errcat.User.New("traffic-manager does not support intercepts that require an extended traffic-agent")
	}
	return err
}

func (s *State) dropAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
) {
	mutator.GetMap(ctx).Delete(wl.GetName(), wl.GetNamespace())
}

func (s *State) restoreAppContainer(ctx context.Context, ii *rpc.InterceptInfo, wl k8sapi.Workload) error {
	clog.Debugf(ctx, "Restoring app container for %s", ii.Id)
	spec := ii.Spec
	n := spec.Agent
	ns := spec.Namespace
	mm := mutator.GetMap(ctx)
	_, err := mm.Update(n, ns, func(sc *agentconfig.Sidecar) (*agentconfig.Sidecar, error) {
		if sc == nil {
			return nil, nil
		}
		var cn *agentconfig.Container
		var err error
		var desiredPolicy agentconfig.ReplacePolicy
		if spec.NoDefaultPort {
			desiredPolicy = agentconfig.ReplacePolicyInactive
			cn, err = icept.FindContainer(sc, spec)
		} else {
			// Let's keep the intercepting agent in place. There might be other intercepts or wiretaps active.
			desiredPolicy = agentconfig.ReplacePolicyIntercept
			cn, _, err = icept.FindIntercept(sc, spec)
		}
		if err != nil || cn.Replace == desiredPolicy {
			// No change is needed. Return the unchanged config rather than nil, because
			// a nil return value would delete the config from the map.
			return sc, nil
		}
		cn.Replace = desiredPolicy

		// The pods for this workload will be killed once the new updated sidecar
		// reaches the configmap. We inactivate them now, so that they don't continue to
		// review intercepts.
		err = mm.EvictPodsWithAgentConfigMismatch(ctx, wl, sc)
		return sc, err
	})
	return err
}

func (s *State) GetOrGenerateAgentConfig(ctx context.Context, name, namespace string) (*agentconfig.Sidecar, error) {
	wl, err := agentmap.GetWorkload(ctx, name, namespace, "")
	if err != nil {
		return nil, grpcErrors.FromError(err, codes.Internal, err.Error())
	}
	return s.getOrCreateAgentConfig(ctx, wl, false, true, nil, agentconfig.ReplacePolicyInactive)
}

func (s *State) generateAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
	agentImage string,
	existing *agentconfig.Sidecar,
) (*agentconfig.Sidecar, error) {
	gc, err := managerutil.GetEnv(ctx).GeneratorConfig(agentImage)
	if err != nil {
		return nil, err
	}
	clog.Debugf(ctx, "generating new agent config for %s", wl)
	sc, err := gc.Generate(ctx, wl, existing)
	if err != nil {
		return nil, err
	}
	if err = s.ValidateCreateAgent(ctx, wl, sc); err != nil {
		return nil, err
	}
	return sc, nil
}

func (s *State) createAgentConfig(ctx context.Context, wl k8sapi.Workload, agentImage string) (*agentconfig.Sidecar, error) {
	return s.generateAgentConfig(ctx, wl, agentImage, nil)
}

func (s *State) getOrCreateAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
	extended bool,
	dryRun bool,
	spec *rpc.InterceptSpec,
	rp agentconfig.ReplacePolicy,
) (*agentconfig.Sidecar, error) {
	enabled, err := checkInterceptAnnotations(ctx, wl)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errcat.User.Newf("%s is not interceptable", wl)
	}

	agentImage := managerutil.GetAgentImage(ctx)
	if err = s.ValidateAgentImage(agentImage, extended); err != nil {
		return nil, err
	}
	mm := mutator.GetMap(ctx)
	if dryRun {
		sc := mm.Get(wl.GetName(), wl.GetNamespace())
		if sc == nil {
			sc, err = s.createAgentConfig(ctx, wl, agentImage)
		}
		return sc, err
	}

	return mm.Update(wl.GetName(), wl.GetNamespace(), func(sc *agentconfig.Sidecar) (*agentconfig.Sidecar, error) {
		if sc != nil {
			// If the agentImage has changed, and the extended image is requested, then update
			if sc.AgentImage != agentImage {
				sc.AgentImage = agentImage
			}
			if serviceScopedIntercept(spec) && !sidecarClaimsService(sc, spec) {
				sc, err = s.generateAgentConfig(ctx, wl, agentImage, sc)
				if err != nil {
					return nil, err
				}
			}
			clog.Debugf(ctx, "found existing agent config for %s", wl)
		} else {
			sc, err = s.createAgentConfig(ctx, wl, agentImage)
			if err != nil {
				return nil, err
			}
		}

		if spec != nil {
			var cn *agentconfig.Container
			if spec.NoDefaultPort {
				cn, err = icept.FindContainer(sc, spec)
			} else {
				cn, _, err = icept.FindIntercept(sc, spec)
			}
			if err != nil {
				return nil, err
			}
			cn.Replace = rp
		}
		return sc, nil
	})
}

func checkInterceptAnnotations(ctx context.Context, wl k8sapi.Workload) (bool, error) {
	pod := wl.GetPodTemplate()
	a := pod.Annotations
	if a == nil {
		return true, nil
	}

	webhookEnabled := true
	manuallyManaged := annotation.GetAnnotation(ctx, pod.Annotations, annotation.ManuallyInjected, annotation.LegacyManuallyInjected) == "true"
	ia := annotation.GetAnnotation(ctx, a, annotation.InjectTrafficAgent, annotation.LegacyInjectTrafficAgent)
	switch ia {
	case "":
		webhookEnabled = !manuallyManaged
	case "enabled":
	case "false", "disabled":
		webhookEnabled = false
	default:
		return false, errcat.User.Newf(
			"%s is not a valid value for the %s.%s/%s annotation",
			ia, wl.GetName(), wl.GetNamespace(), annotation.InjectTrafficAgent)
	}

	if !manuallyManaged {
		return webhookEnabled, nil
	}
	cns := pod.Spec.Containers
	var an *core.Container
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentconfig.ContainerName {
			an = cn
			break
		}
	}
	if an == nil {
		return false, errcat.User.Newf(
			"annotation %s.%s/%s=true but pod has no traffic-agent container",
			wl.GetName(), wl.GetNamespace(), annotation.ManuallyInjected)
	}
	return true, nil
}

// agentSessionMatches reports whether agent is the one waitForAgents is
// waiting for: same name and namespace, and the same node-agent/sidecar
// kind. The kind check matters because a sidecar agent and a node-agent Job
// for the same workload can both be registering sessions at once, and each
// wait must only be satisfied by its own kind.
func agentSessionMatches(agent *AgentSession, name, namespace string, nodeAgent bool) bool {
	return agent.Name == name && agent.Namespace == namespace && agent.NodeAgent == nodeAgent
}

// terminalEventMessage turns a terminal warning event into the user-facing
// message for a failed agent arrival, enriching a BackOff event with the
// failing container's log and appending a hint that matches the agent kind.
func terminalEventMessage(ctx context.Context, fe *events.Event, podNamespace string, failedContainerRx *regexp.Regexp, nodeAgent bool) string {
	msg := fe.Note
	switch fe.Reason {
	case "BackOff":
		// The traffic-agent container was injected, but it fails to start
		if rr := failedContainerRx.FindStringSubmatch(msg); rr != nil {
			cn := rr[1]
			pod := rr[2]
			rq := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(podNamespace).GetLogs(pod, &core.PodLogOptions{
				Container: cn,
			})
			if rs, err := rq.Stream(ctx); err == nil {
				if log, err := io.ReadAll(rs); err == nil {
					clog.Infof(ctx, "Log from failing pod %q, container %s\n%s", pod, cn, string(log))
				} else {
					clog.Errorf(ctx, "failed to read log stream from pod %q, container %s\n%s", pod, cn, err)
				}
				_ = rs.Close()
			} else {
				clog.Errorf(ctx, "failed to read log from pod %q, container %s\n%s", pod, cn, err)
			}
		}
		msg = fmt.Sprintf("%s\nThe logs of %s %s might provide more details", msg, fe.Regarding.Kind, fe.Regarding.Name)
	case "Failed", "FailedCreate", "FailedScheduling":
		if nodeAgent {
			// The node-agent Job's pod could not be admitted or scheduled.
			msg = fmt.Sprintf(
				"%s\nHint: the node-agent Job could not start. If the traffic-manager's namespace enforces Pod Security Admission, it must permit privileged pods for node-agent mode.",
				msg)
		} else {
			// The injection of the traffic-agent failed for some reason, most likely due to resource quota restrictions.
			msg = fmt.Sprintf(
				"%s\nHint: if the error mentions resource quota, the traffic-agent's requested resources can be configured by providing values to telepresence helm install",
				msg)
		}
	}
	return msg
}

// waitForAgents waits for expected distinct agent sessions matching name,
// namespace and nodeAgent to register, accumulating matching, non-
// blacklisted sessions by PodUid across every delta (including the initial
// snapshot Subscribe delivers of agents already registered) until that many
// are present.
func (s *State) waitForAgents(
	ctx context.Context,
	name, namespace string,
	nodeAgent bool,
	expected int,
	failedCreateCh <-chan *events.Event,
) ([]*AgentSession, error) {
	clog.Debugf(ctx, "Waiting for %d agent(s) %s.%s", expected, name, namespace)
	deltaCh := s.WatchAgents(ctx, func(_ tunnel.SessionID, agent *AgentSession) bool {
		return agentSessionMatches(agent, name, namespace, nodeAgent)
	})
	// podNamespace is where the pods behind failure events live: the
	// workload's namespace for a sidecar, but the traffic-manager's own
	// namespace for a node-agent Job.
	podNamespace := namespace
	if nodeAgent {
		podNamespace = managerutil.GetEnv(ctx).ManagerNamespace
	}
	failedContainerRx := regexp.MustCompile(`restarting failed container (\S+) in pod ([0-9A-Za-z_-]+)_` + podNamespace)
	mm := mutator.GetMap(ctx)

	// fes collects events from the failedCreatedCh and is included in the error message in case
	// the waitForAgents call times out.
	var fes []*events.Event
	// arrived accumulates matching, non-blacklisted sessions by PodUid. It
	// is returned even alongside a ctx.Done() error, so the partial set of
	// agents that did register remains observable.
	arrived := make(map[string]*AgentSession)
	for {
		select {
		case fe, ok := <-failedCreateCh:
			if !ok {
				return nil, errors.New("failed create channel closed")
			}
			if !eventwatch.IsTerminal(fe) {
				// Something went wrong, but it might not be fatal. There are several events logged that are
				// just warnings where the action will be retried and eventually succeed. Collect them for
				// the timeout message and keep waiting.
				fes = append(fes, fe)
				continue
			}
			// A terminal event was encountered. Surface it now with agent-specific context, rather than
			// making the user wait for a timeout.
			return nil, errcat.User.New(terminalEventMessage(ctx, fe, podNamespace, failedContainerRx, nodeAgent))
		case delta, ok := <-deltaCh:
			if !ok {
				// The request has been canceled.
				return nil, status.Error(codes.Canceled, fmt.Sprintf("channel closed while waiting for agent %s.%s to arrive", name, namespace))
			}
			for _, a := range delta.Upserts {
				if mm.IsInactive(k8sTypes.UID(a.PodUid)) {
					clog.Debugf(ctx, "Agent %s(%s) is blacklisted", a.PodName, a.PodIp)
					continue
				}
				clog.Debugf(ctx, "Agent %s(%s) is ready", a.PodName, a.PodIp)
				arrived[a.PodUid] = a
			}
			if len(arrived) >= expected {
				as := make([]*AgentSession, 0, len(arrived))
				for _, a := range arrived {
					as = append(as, a)
				}
				return as, nil
			}
		case <-ctx.Done():
			v := "canceled"
			if ctx.Err() == context.DeadlineExceeded {
				v = "timed out"
			}
			bf := &strings.Builder{}
			ioutil.Printf(bf, "request %s while waiting for agent %s.%s to arrive", v, name, namespace)
			if len(fes) > 0 {
				bf.WriteString(": Events that may be relevant:\n")
				eventwatch.WriteList(bf, fes)
			} else if ctx.Err() == context.DeadlineExceeded {
				if nodeAgent {
					// No failure events were observed, yet no agent arrived. Unlike
					// the sidecar path, there's no injector webhook to blame; point
					// at the Job and its pod instead.
					ioutil.Printf(bf,
						"\nThe node-agent Job's pod either failed to start or its agent failed to register. "+
							"Inspect the Job (kubectl get jobs -n %s -l app=traffic-node-agent) and its pod's logs. "+
							"If the namespace enforces Pod Security Admission, node-agent Jobs require the privileged profile.",
						podNamespace)
				} else {
					// No injection failures were observed, yet no agent arrived. This is the typical
					// signature of the API server being unable to reach the agent-injector webhook
					// (the pods are admitted unmodified because the webhook's failurePolicy is Ignore).
					bf.WriteString(
						"\nThe pods were created without a traffic-agent. This usually means the Kubernetes API server " +
							"cannot reach the agent-injector webhook. On EKS with the Calico CNI, install or upgrade the " +
							"traffic-manager with hostNetwork=true, or expose the webhook externally (agentInjector.webhook.url). " +
							"See https://telepresence.io/docs/troubleshooting#eks-calico-and-traffic-agent-injection-timeouts")
				}
			}
			as := make([]*AgentSession, 0, len(arrived))
			for _, a := range arrived {
				as = append(as, a)
			}
			return as, errcat.User.New(bf.String())
		}
	}
}
