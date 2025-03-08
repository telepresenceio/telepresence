package state

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
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
func (s *state) PrepareIntercept(
	ctx context.Context,
	cr *rpc.CreateInterceptRequest,
) (pi *rpc.PreparedIntercept, err error) {
	interceptError := func(err error) (*rpc.PreparedIntercept, error) {
		dlog.Errorf(ctx, "PrepareIntercept error %v", err)
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
			dlog.Error(ctx, err)
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
			dlog.Error(ctx, err)
			return interceptError(err)
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
		if cn, err = findContainer(ac, spec); err == nil {
			pi.ContainerName = cn.Name
			pi.ServiceName = ""
			if spec.PortIdentifier == "all" {
				prepareAllContainerPorts(cn, pi)
			} else if spec.PortIdentifier != "" {
				err = s.preparePorts(ac, cn, cr, pi)
			}
		}
	} else {
		err = s.preparePorts(ac, nil, cr, pi)
	}

	if err != nil {
		return interceptError(errcat.User.New(err))
	}
	return pi, nil
}

func prepareAllContainerPorts(cn *agentconfig.Container, pi *rpc.PreparedIntercept) {
	pics := agentconfig.PortUniqueIntercepts(cn)
	if ni := len(pics); ni > 0 {
		// Put the first port in the intercept itself
		i0 := pics[0]
		pi.ContainerPort = int32(i0.ContainerPort)
		pi.Protocol = string(i0.Protocol)
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

func (s *state) preparePorts(ac *agentconfig.Sidecar, cn *agentconfig.Container, cr *rpc.CreateInterceptRequest, pi *rpc.PreparedIntercept) (err error) {
	spec := cr.InterceptSpec
	portID := agentconfig.PortIdentifier(spec.PortIdentifier)
	containerOnly := cn != nil

	var ic *agentconfig.Intercept
	if containerOnly {
		ic, err = findContainerIntercept(ac, cn, portID)
	} else {
		cn, ic, err = findIntercept2(ac, pi.ServiceName, pi.ContainerName, portID)
	}
	if err != nil {
		return err
	}

	uniqueContainerPorts := make(map[agentconfig.PortAndProto]struct{})
	uniqueContainerPorts[agentconfig.PortAndProto{Proto: ic.Protocol, Port: ic.ContainerPort}] = struct{}{}

	var podPorts []string
	if len(spec.PodPorts) > 0 {
		uniqueTargets := make(map[agentconfig.PortAndProto]struct{})
		uniqueTargets[agentconfig.PortAndProto{Proto: ic.Protocol, Port: uint16(spec.TargetPort)}] = struct{}{}
		podPorts = make([]string, len(spec.PodPorts))
		for i, pms := range spec.PodPorts {
			pm := agentconfig.PortMapping(pms)
			var pmIc *agentconfig.Intercept
			if containerOnly {
				pmIc, err = findContainerIntercept(ac, cn, pm.From())
			} else {
				_, pmIc, err = findIntercept2(ac, spec.ServiceName, spec.ContainerName, pm.From())
			}
			if err != nil {
				return err
			}

			to := pm.To()
			if _, ok := uniqueTargets[to]; ok {
				return fmt.Errorf("multiple port definitions targeting %s", to)
			}
			uniqueTargets[to] = struct{}{}

			from := agentconfig.PortAndProto{Proto: pmIc.Protocol, Port: pmIc.ContainerPort}
			if _, ok := uniqueContainerPorts[from]; ok {
				return fmt.Errorf("multiple port definitions using container port %s", from)
			}
			uniqueContainerPorts[from] = struct{}{}

			// Return the resolved numeric container port.
			podPorts[i] = fmt.Sprintf("%d:%s", pmIc.ContainerPort, to)
		}
	}

	// Validate that there's no port conflict with other intercepts using the same agent.
	otherIcs := s.intercepts.LoadMatching(func(s string, info *Intercept) bool {
		return info.Disposition == rpc.InterceptDispositionType_ACTIVE && info.Spec.Agent == ac.AgentName && info.Spec.Namespace == ac.Namespace
	})

	if spec.Mechanism != "http" {
		// Intercept is global, so it will conflict with any other intercept using the same port and protocol.
		for _, otherIc := range otherIcs {
			oSpec := otherIc.Spec // Validate that there's no port conflict
			for cp := range uniqueContainerPorts {
				if cp.Port == uint16(oSpec.ContainerPort) && string(cp.Proto) == oSpec.Protocol {
					name := oSpec.Name
					client := oSpec.Client
					if IsChildIntercept(oSpec) {
						if cps := strings.Fields(client); len(cps) == 4 {
							name = cps[2]
							client = cps[3]
						}
					}
					return fmt.Errorf("container port %d is already intercepted by %s, intercept %s", cp.Port, client, name)
				}
			}
		}
	}
	pi.ContainerName = cn.Name
	pi.ServiceUid = string(ic.ServiceUID)
	pi.ServicePortName = ic.ServicePortName
	pi.Protocol = string(ic.Protocol)
	pi.ContainerPort = int32(ic.ContainerPort)
	pi.ServicePort = int32(ic.ServicePort)
	pi.PodPorts = podPorts
	return nil
}

func (s *state) AddIntercept(ctx context.Context, cir *rpc.CreateInterceptRequest) (*ClientSession, *rpc.InterceptInfo, error) {
	clientSession := cir.Session
	sessionID := tunnel.SessionID(clientSession.SessionId)
	client := s.GetClient(sessionID)
	if client == nil {
		return nil, nil, status.Errorf(codes.NotFound, "session %q not found", sessionID)
	}

	spec := cir.InterceptSpec
	interceptID := fmt.Sprintf("%s:%s", sessionID, spec.Name)

	wl, err := agentmap.GetWorkload(ctx, spec.Agent, spec.Namespace, k8sapi.Kind(spec.WorkloadKind))
	if err != nil {
		code := codes.Internal
		if k8sErrors.IsNotFound(err) {
			code = codes.NotFound
		}
		return nil, nil, status.Error(code, err.Error())
	}

	rp := agentconfig.ReplacePolicyIntercept
	if spec.Replace {
		rp = agentconfig.ReplacePolicyContainer
	}
	_, _, err = s.ensureAgent(ctx, wl, s.isExtended(spec), false, spec, rp)
	if err != nil {
		return nil, nil, err
	}

	is, err := s.addIntercept(interceptID, cir)
	if err != nil {
		return nil, nil, err
	}

	// Add one child intercept for each pod-port.
	for _, pms := range spec.PodPorts {
		pm := agentconfig.PortMapping(pms)
		from, to, err := pm.FromNumberAndTo()
		if err != nil {
			// Did PrepareIntercept create an invalid pod_port?
			return nil, nil, status.Errorf(codes.Internal, "invalid pod_port %q: %v", pm, err)
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
		pmSpec.TargetPort = int32(pm.To().Port)

		// The Client field helps IsChildIntercept identify the child.
		pmSpec.Client = fmt.Sprintf("child %s %s %s", pm, spec.Name, spec.Client)

		pmInterceptID := fmt.Sprintf("%s:%s", sessionID, pmSpec.Name)
		_, err = s.addIntercept(pmInterceptID, pmCir)
		if err != nil {
			return nil, nil, err
		}

		// Add finalizer to the interceptState of the parent intercept.
		is.addFinalizer(func(_ context.Context, _ *rpc.InterceptInfo) error {
			s.intercepts.LoadAndDelete(pmInterceptID)
			return nil
		})
	}
	err = s.AddInterceptFinalizer(interceptID, func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
		return s.restoreAppContainer(ctx, interceptInfo, wl)
	})
	if err != nil {
		dlog.Errorf(ctx, "Failed to add finalizer for %s: %v", interceptID, err)
	}
	return client, is.InterceptInfo, nil
}

func IsChildIntercept(spec *rpc.InterceptSpec) bool {
	return strings.HasPrefix(spec.Client, "child ")
}

func (s *state) addIntercept(id string, cir *rpc.CreateInterceptRequest) (*Intercept, error) {
	is := s.self.NewInterceptInfo(id, cir)

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
			return nil, status.Errorf(codes.AlreadyExists, "Intercept named %q already exists", is.Spec.Name)
		}
		s.intercepts.Store(id, is)
	}
	return is, nil
}

func (s *state) NewInterceptInfo(interceptID string, ciReq *rpc.CreateInterceptRequest) *Intercept {
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

func (s *state) AddInterceptFinalizer(interceptID string, finalizer InterceptFinalizer) error {
	is, ok := s.intercepts.Load(interceptID)
	if !ok {
		return status.Errorf(codes.NotFound, "no such intercept %s", interceptID)
	}
	is.addFinalizer(finalizer)
	return nil
}

// getAgentsInterceptedByClient returns the session IDs for each agent that are currently
// intercepted by the client with the given client session ID.
func (s *state) getAgentsInterceptedByClient(clientSessionID tunnel.SessionID) map[tunnel.SessionID]*AgentSession {
	intercepts := s.intercepts.LoadMatching(func(_ string, ii *Intercept) bool {
		return ii.ClientSession.SessionId == string(clientSessionID)
	})
	if len(intercepts) == 0 {
		return nil
	}
	return s.LoadMatchingAgents(func(_ tunnel.SessionID, ai *AgentSession) bool {
		for _, ii := range intercepts {
			if ai.Name == ii.Spec.Agent && ai.Namespace == ii.Spec.Namespace {
				return true
			}
		}
		return false
	})
}

func (s *state) EnsureAgent(ctx context.Context, n, ns string) (as []*AgentSession, err error) {
	var wl k8sapi.Workload
	wl, err = agentmap.GetWorkload(ctx, n, ns, "")
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			err = errcat.User.New(err)
		}
		return nil, err
	}
	_, as, err = s.ensureAgent(ctx, wl, false, false, nil, agentconfig.ReplacePolicyInactive)
	return as, err
}

func (s *state) ValidateCreateAgent(context.Context, k8sapi.Workload, agentconfig.SidecarExt) error {
	return nil
}

// sortAgents will sort the given AgentInfo based on pod name.
func sortAgents(as []*AgentSession) {
	sort.Slice(as, func(i, j int) bool {
		return as[i].PodName < as[j].PodName
	})
}

func (s *state) ensureAgent(parentCtx context.Context, wl k8sapi.Workload, extended, dryRun bool, spec *rpc.InterceptSpec, rp agentconfig.ReplacePolicy) (
	ac *agentconfig.Sidecar, as []*AgentSession, err error,
) {
	if agentmap.TrafficManagerSelector.Matches(labels.Set(wl.GetLabels())) {
		msg := fmt.Sprintf("%s is the Telepresence Traffic Manager. It can not have a traffic-agent", wl)
		dlog.Error(parentCtx, msg)
		return nil, nil, status.Error(codes.FailedPrecondition, msg)
	}

	if !managerutil.AgentInjectorEnabled(parentCtx) {
		cfgJSON, ok := wl.GetPodTemplate().Annotations[agentconfig.ConfigAnnotation]
		if !ok {
			msg := fmt.Sprintf("agent-injector is disabled and no agent has been added manually for %s", wl)
			return nil, nil, status.Error(codes.FailedPrecondition, msg)
		}
		sce, err := agentconfig.UnmarshalJSON(cfgJSON)
		if err != nil {
			return nil, nil, err
		}
		ac = sce.AgentConfig()
		am := s.LoadMatchingAgents(func(_ tunnel.SessionID, ai *AgentSession) bool {
			return ai.Name == ac.AgentName && ai.Namespace == ac.Namespace
		})
		as = make([]*AgentSession, len(am))
		i := 0
		for _, found := range am {
			as[i] = found
			i++
		}
		sortAgents(as)
		return ac, as, nil
	}

	if dryRun {
		sce, err := s.getOrCreateAgentConfig(parentCtx, wl, extended, dryRun, spec, rp)
		if err != nil {
			return nil, nil, err
		}
		return sce.AgentConfig(), nil, nil
	}

	ctx, cancel := context.WithTimeout(parentCtx, managerutil.GetEnv(parentCtx).AgentArrivalTimeout)
	defer cancel()

	failedCreateCh, err := watchFailedInjectionEvents(ctx, wl.GetName(), wl.GetNamespace())
	if err != nil {
		return nil, nil, err
	}

	sce, err := s.getOrCreateAgentConfig(ctx, wl, extended, dryRun, spec, rp)
	if err != nil {
		return nil, nil, err
	}
	err = mutator.GetMap(ctx).EvictPodsWithAgentConfigMismatch(ctx, wl, sce)
	if err != nil {
		dlog.Errorf(ctx, "failed to inactivate pods: %v", err)
		return nil, nil, err
	}
	ac = sce.AgentConfig()
	if as, err = s.waitForAgents(ctx, ac, failedCreateCh); err != nil {
		// If no agent arrives, then drop its entry from the configmap. This ensures that there
		// are no false positives the next time an intercept is attempted.
		s.dropAgentConfig(parentCtx, wl)
		return nil, nil, err
	}
	sortAgents(as)
	return ac, as, nil
}

func (s *state) isExtended(spec *rpc.InterceptSpec) bool {
	return spec.Mechanism != "tcp"
}

func (s *state) ValidateAgentImage(agentImage string, extended bool) (err error) {
	if agentImage == "" {
		err = errcat.User.Newf(
			"intercepts are disabled because the traffic-manager is unable to determine what image to use for injected traffic-agents.")
	} else if extended {
		err = errcat.User.New("traffic-manager does not support intercepts that require an extended traffic-agent")
	}
	return err
}

func (s *state) dropAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
) {
	mutator.GetMap(ctx).Delete(wl.GetName(), wl.GetNamespace())
}

func (s *state) restoreAppContainer(ctx context.Context, ii *rpc.InterceptInfo, wl k8sapi.Workload) error {
	dlog.Debugf(ctx, "Restoring app container for %s", ii.Id)
	spec := ii.Spec
	n := spec.Agent
	ns := spec.Namespace
	mm := mutator.GetMap(ctx)
	_, err := mm.Update(n, ns, func(sce agentconfig.SidecarExt) (ext agentconfig.SidecarExt, err error) {
		if sce == nil {
			return nil, nil
		}
		var cn *agentconfig.Container
		if spec.NoDefaultPort {
			cn, err = findContainer(sce.AgentConfig(), spec)
		} else {
			cn, _, err = findIntercept(sce.AgentConfig(), spec)
		}
		if err != nil {
			return nil, nil
		}
		if cn.Replace == agentconfig.ReplacePolicyInactive {
			return nil, nil
		}
		cn.Replace = agentconfig.ReplacePolicyInactive

		// The pods for this workload will be killed once the new updated sidecar
		// reaches the configmap. We inactivate them now, so that they don't continue to
		// review intercepts.
		err = mm.EvictPodsWithAgentConfigMismatch(ctx, wl, sce)
		return sce, err
	})
	return err
}

func (s *state) GetOrGenerateAgentConfig(ctx context.Context, name, namespace string) (agentconfig.SidecarExt, error) {
	wl, err := agentmap.GetWorkload(ctx, name, namespace, "")
	if err != nil {
		code := codes.Internal
		if k8sErrors.IsNotFound(err) {
			code = codes.NotFound
		}
		return nil, status.Error(code, err.Error())
	}
	return s.getOrCreateAgentConfig(ctx, wl, false, true, nil, agentconfig.ReplacePolicyInactive)
}

func (s *state) createAgentConfig(ctx context.Context, wl k8sapi.Workload, agentImage string) (sce agentconfig.SidecarExt, err error) {
	var gc agentmap.GeneratorConfig
	if gc, err = agentmap.GeneratorConfigFunc(agentImage); err != nil {
		return nil, err
	}
	dlog.Debugf(ctx, "generating new agent config for %s", wl)
	if sce, err = gc.Generate(ctx, wl, nil); err != nil {
		return nil, err
	}
	if err = s.self.ValidateCreateAgent(ctx, wl, sce); err != nil {
		return nil, err
	}
	return sce, nil
}

func (s *state) getOrCreateAgentConfig(
	ctx context.Context,
	wl k8sapi.Workload,
	extended bool,
	dryRun bool,
	spec *rpc.InterceptSpec,
	rp agentconfig.ReplacePolicy,
) (sce agentconfig.SidecarExt, err error) {
	enabled, err := checkInterceptAnnotations(wl)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errcat.User.Newf("%s is not interceptable", wl)
	}

	agentImage := managerutil.GetAgentImage(ctx)
	if err = s.self.ValidateAgentImage(agentImage, extended); err != nil {
		return nil, err
	}
	mm := mutator.GetMap(ctx)
	if dryRun {
		sce = mm.Get(wl.GetName(), wl.GetNamespace())
		if sce == nil {
			sce, err = s.createAgentConfig(ctx, wl, agentImage)
		}
		return sce, err
	}

	return mm.Update(wl.GetName(), wl.GetNamespace(), func(sce agentconfig.SidecarExt) (agentconfig.SidecarExt, error) {
		var ac *agentconfig.Sidecar
		if sce != nil {
			ac = sce.AgentConfig()
			// If the agentImage has changed, and the extended image is requested, then update
			if ac.AgentImage != agentImage {
				ac.AgentImage = agentImage
			}
			dlog.Debugf(ctx, "found existing agent config for %s", wl)
		} else {
			sce, err = s.createAgentConfig(ctx, wl, agentImage)
			if err != nil {
				return nil, err
			}
			ac = sce.AgentConfig()
		}

		if spec != nil {
			var cn *agentconfig.Container
			if spec.NoDefaultPort {
				cn, err = findContainer(ac, spec)
			} else {
				cn, _, err = findIntercept(ac, spec)
			}
			if err != nil {
				return nil, err
			}
			cn.Replace = rp
		}

		if dryRun {
			dlog.Debugf(ctx, "dry run for getOrCreateAgentConfig %s returns", wl)
			return sce, nil
		}
		return sce, nil
	})
}

func checkInterceptAnnotations(wl k8sapi.Workload) (bool, error) {
	pod := wl.GetPodTemplate()
	a := pod.Annotations
	if a == nil {
		return true, nil
	}

	webhookEnabled := true
	manuallyManaged := a[agentconfig.ManualInjectAnnotation] == "true"
	ia := a[agentconfig.InjectAnnotation]
	switch ia {
	case "":
		webhookEnabled = !manuallyManaged
	case "enabled":
	case "false", "disabled":
		webhookEnabled = false
	default:
		return false, errcat.User.Newf(
			"%s is not a valid value for the %s.%s/%s annotation",
			ia, wl.GetName(), wl.GetNamespace(), agentconfig.ManualInjectAnnotation)
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
			wl.GetName(), wl.GetNamespace(), agentconfig.ManualInjectAnnotation)
	}
	return true, nil
}

func watchFailedInjectionEvents(ctx context.Context, name, namespace string) (<-chan *events.Event, error) {
	// A timestamp with second granularity is needed here, because that's what the event creation time uses.
	// Finer granularity will result in relevant events seemingly being created before this timestamp because
	// they have the fraction of seconds trimmed off (which is odd, given that the type used is a MicroTime).
	start := time.Unix(time.Now().Unix(), 0)

	ei := k8sapi.GetK8sInterface(ctx).EventsV1().Events(namespace)
	w, err := ei.Watch(ctx, meta.ListOptions{
		FieldSelector: fields.OneTermNotEqualSelector("type", "Normal").String(),
	})
	if err != nil {
		return nil, err
	}
	nd := name + "-"
	ec := make(chan *events.Event)
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case eo, ok := <-w.ResultChan():
				if !ok {
					return
				}
				// Using negated Before when comparing the timestamps here is relevant. They will often be equal and still relevant
				if e, ok := eo.Object.(*events.Event); ok &&
					!e.CreationTimestamp.Time.Before(start) &&
					!strings.HasPrefix(e.Note, "(combined from similar events):") {
					n := e.Regarding.Name
					if strings.HasPrefix(n, nd) || n == name {
						dlog.Infof(ctx, "%s %s %s", e.Type, e.Reason, e.Note)
						ec <- e
					}
				}
			}
		}
	}()
	return ec, nil
}

func (s *state) waitForAgents(ctx context.Context, ac *agentconfig.Sidecar, failedCreateCh <-chan *events.Event) ([]*AgentSession, error) {
	name := ac.AgentName
	namespace := ac.Namespace
	dlog.Debugf(ctx, "Waiting for agent %s.%s", name, namespace)
	snapshotCh := s.WatchAgents(ctx, func(_ tunnel.SessionID, agent *AgentSession) bool {
		return agent.Name == name && agent.Namespace == namespace
	})
	failedContainerRx := regexp.MustCompile(`restarting failed container (\S+) in pod ([0-9A-Za-z_-]+)_` + namespace)
	mm := mutator.GetMap(ctx)

	// fes collects events from the failedCreatedCh and is included in the error message in case
	// the waitForAgents call times out.
	var fes []*events.Event
	for {
		select {
		case fe, ok := <-failedCreateCh:
			if !ok {
				return nil, errors.New("failed create channel closed")
			}
			msg := fe.Note
			// Terminate directly on known fatal events. No need for the user to wait for a timeout
			// when one of those is encountered.
			switch fe.Reason {
			case "BackOff":
				// The traffic-agent container was injected, but it fails to start
				if rr := failedContainerRx.FindStringSubmatch(msg); rr != nil {
					cn := rr[1]
					pod := rr[2]
					rq := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(namespace).GetLogs(pod, &core.PodLogOptions{
						Container: cn,
					})
					if rs, err := rq.Stream(ctx); err == nil {
						if log, err := io.ReadAll(rs); err == nil {
							dlog.Infof(ctx, "Log from failing pod %q, container %s\n%s", pod, cn, string(log))
						} else {
							dlog.Errorf(ctx, "failed to read log stream from pod %q, container %s\n%s", pod, cn, err)
						}
						_ = rs.Close()
					} else {
						dlog.Errorf(ctx, "failed to read log from pod %q, container %s\n%s", pod, cn, err)
					}
				}
				msg = fmt.Sprintf("%s\nThe logs of %s %s might provide more details", msg, fe.Regarding.Kind, fe.Regarding.Name)
			case "Failed", "FailedCreate", "FailedScheduling":
				// The injection of the traffic-agent failed for some reason, most likely due to resource quota restrictions.
				if fe.Type == "Warning" && (strings.Contains(msg, "waiting for ephemeral volume") ||
					strings.Contains(msg, "unbound immediate PersistentVolumeClaims") ||
					strings.Contains(msg, "skip schedule deleting pod") ||
					strings.Contains(msg, "nodes are available")) {
					// This isn't fatal.
					fes = append(fes, fe)
					continue
				}
				msg = fmt.Sprintf(
					"%s\nHint: if the error mentions resource quota, the traffic-agent's requested resources can be configured by providing values to telepresence helm install",
					msg)
			default:
				// Something went wrong, but it might not be fatal. There are several events logged that are just
				// warnings where the action will be retried and eventually succeed.
				fes = append(fes, fe)
				continue
			}
			return nil, errcat.User.New(msg)
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				return nil, status.Error(codes.Canceled, fmt.Sprintf("channel closed while waiting for agent %s.%s to arrive", name, namespace))
			}
			if len(snapshot) == 0 {
				continue
			}
			as := make([]*AgentSession, 0, len(snapshot))
			for _, a := range snapshot {
				if mm.IsInactive(types.UID(a.PodUid)) {
					dlog.Debugf(ctx, "Agent %s(%s) is blacklisted", a.PodName, a.PodIp)
				} else {
					dlog.Debugf(ctx, "Agent %s(%s) is ready", a.PodName, a.PodIp)
					as = append(as, a)
					break
				}
			}
			if len(as) > 0 {
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
				writeEventList(bf, fes)
			}
			return nil, errcat.User.New(bf.String())
		}
	}
}

func writeEventList(bf *strings.Builder, es []*events.Event) {
	now := time.Now()
	age := func(e *events.Event) string {
		return now.Sub(e.CreationTimestamp.Time).Truncate(time.Second).String()
	}
	object := func(e *events.Event) string {
		or := e.Regarding
		return strings.ToLower(or.Kind) + "/" + or.Name
	}
	ageLen, typeLen, reasonLen, objectLen := len("AGE"), len("TYPE"), len("REASON"), len("OBJECT")
	for _, e := range es {
		if l := len(age(e)); l > ageLen {
			ageLen = l
		}
		if l := len(e.Type); l > typeLen {
			typeLen = l
		}
		if l := len(e.Reason); l > reasonLen {
			reasonLen = l
		}
		if l := len(object(e)); l > objectLen {
			objectLen = l
		}
	}
	ageLen += 3
	typeLen += 3
	reasonLen += 3
	objectLen += 3
	ioutil.Printf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, "AGE", typeLen, "TYPE", reasonLen, "REASON", objectLen, "OBJECT", "MESSAGE")
	for _, e := range es {
		ioutil.Printf(bf, "%-*s%-*s%-*s%-*s%s\n", ageLen, age(e), typeLen, e.Type, reasonLen, e.Reason, objectLen, object(e), e.Note)
	}
}

// findContainer finds the container configuration that matches the given InterceptSpec.
func findContainer(ac *agentconfig.Sidecar, spec *rpc.InterceptSpec) (foundCN *agentconfig.Container, err error) {
	if spec.ContainerName == "" {
		if len(ac.Containers) == 1 {
			return ac.Containers[0], nil
		}
		return nil, errcat.User.Newf("%s %s.%s has more than one container",
			ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
	}
	for _, cn := range ac.Containers {
		if spec.ContainerName == cn.Name {
			return cn, nil
		}
	}
	return nil, errcat.User.Newf("%s %s.%s has no container named %s",
		ac.WorkloadKind, ac.WorkloadName, ac.Namespace, spec.ContainerName)
}

// findIntercept finds the intercept configuration that matches the given InterceptSpec's service/service port or container port.
func findIntercept(ac *agentconfig.Sidecar, spec *rpc.InterceptSpec) (foundCN *agentconfig.Container, foundIC *agentconfig.Intercept, err error) {
	return findIntercept2(ac, spec.ServiceName, spec.ContainerName, agentconfig.PortIdentifier(spec.PortIdentifier))
}

// findIntercept finds the intercept configuration that matches the given InterceptSpec's service/service port or container port.
func findIntercept2(ac *agentconfig.Sidecar, serviceName, containerName string, pi agentconfig.PortIdentifier) (
	foundCN *agentconfig.Container, foundIC *agentconfig.Intercept, err error,
) {
	for _, cn := range ac.Containers {
		for _, ic := range cn.Intercepts {
			if !(serviceName == "" || serviceName == ic.ServiceName) {
				continue
			}
			if pi != "" {
				if ic.ServiceUID != "" {
					if !agentconfig.IsInterceptForService(pi, ic) {
						continue
					}
				} else if !agentconfig.IsInterceptForContainer(pi, ic) {
					continue
				}
			}
			if foundIC == nil {
				foundCN = cn
				if containerName != "" {
					for _, cx := range ac.Containers {
						if cx.Name == containerName {
							foundCN = cx
							break
						}
					}
				}
				foundIC = ic
				continue
			}
			var msg string
			switch {
			case serviceName == "" && pi == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable ports.\n"+
					"Please specify the service and/or port you want to intercept "+
					"by passing the --service=<svc> and/or --port=<local:portName/portNumber> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
			case serviceName == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable services with port %s.\n"+
					"Please specify the service you want to intercept by passing the --service=<svc> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, pi)
			case pi == "":
				msg = fmt.Sprintf("%s %s.%s has multiple interceptable ports in service %s.\n"+
					"Please specify the port you want to intercept by passing the --port=<local:svcPortName> flag.",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, serviceName)
			default:
				msg = fmt.Sprintf("%s %s.%s intercept config is broken. Service %s, port %s is declared more than once\n",
					ac.WorkloadKind, ac.WorkloadName, ac.Namespace, serviceName, pi)
			}
			return nil, nil, errcat.User.New(msg)
		}
	}
	if foundIC != nil {
		return foundCN, foundIC, nil
	}

	ss := ""
	if serviceName != "" {
		if pi != "" {
			ss = fmt.Sprintf(" matching service %s, port %s", serviceName, pi)
		} else {
			ss = fmt.Sprintf(" matching service %s", serviceName)
		}
	} else if pi != "" {
		ss = fmt.Sprintf(" matching port %s", pi)
	}
	return nil, nil, errcat.User.Newf("%s %s.%s has no interceptable port%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace, ss)
}

// findContainerIntercept finds the intercept configuration that matches container port.
func findContainerIntercept(ac *agentconfig.Sidecar, cn *agentconfig.Container, pi agentconfig.PortIdentifier) (*agentconfig.Intercept, error) {
	for _, ic := range cn.Intercepts {
		if agentconfig.IsInterceptForContainer(pi, ic) {
			return ic, nil
		}
	}
	return nil, errcat.User.Newf("%s %s.%s has no container port matching %s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace, pi)
}
