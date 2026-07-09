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
		// A node-agent Job is reaped (see reapNodeAgentJobs in nodeagent.go)
		// whenever any node-agent intercept of the agent ends, because Jobs
		// aren't shared across concurrent intercepts yet. A second concurrent
		// node-agent intercept on the same workload must therefore be
		// refused, or ending the first would tear down the Job the second
		// one still depends on.
		iid := fmt.Sprintf("%s:%s", client.id, spec.Name)
		if existing := s.activeNodeAgentIntercept(spec, iid); existing != nil {
			return interceptError(errcat.User.Newf(
				"%s.%s already has a node-agent intercept named %q created by client %q; node-agent mode supports only one intercept per workload",
				spec.Agent, spec.Namespace, existing.Spec.Name, existing.Spec.Client))
		}
	} else {
		// Provisioning a sidecar intercept injects a traffic-agent into the
		// workload's pod template and restarts its pods. A live node-agent
		// intercept depends on the specific pod (and its CRI container IDs)
		// that is currently running, so serving this request would break it
		// out from under its client. exceptID is irrelevant for a non-
		// node-agent spec (it can never equal a node-agent intercept's id),
		// but it's passed for uniformity with the branch above.
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
		if err = s.ensureNodeAgent(ctx, wl, ac); err != nil {
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
	err = s.checkInterceptConflicts(ac, client, containerPorts, spec)
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
	if spec.Wiretap {
		// A wiretap intercept is never in conflict with any other intercept.
		return nil
	}

	// Validate that there's no port conflict with other intercepts using the same agent.
	potentialConflicts := s.intercepts.LoadMatching(func(s string, info *Intercept) bool {
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
	} else {
		_, _, err = s.ensureAgent(ctx, wl, s.isExtended(spec), false, spec, rp)
		if err != nil {
			return nil, nil, err
		}
	}

	is, err := s.addIntercept(interceptID, cir)
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
	}
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

func (s *State) addIntercept(id string, cir *rpc.CreateInterceptRequest) (*Intercept, error) {
	is := s.NewInterceptInfo(id, cir)

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
		if err = s.ensureNodeAgent(ctx, wl, sc); err != nil {
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
		as, err = s.waitForNodeAgent(ctx, n, ns)
		if err != nil {
			s.removeLease(sessionID, n, ns)
			return nil, err
		}
		return as, nil
	}

	_, as, err = s.ensureAgent(ctx, wl, false, false, nil, agentconfig.ReplacePolicyInactive)
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
	as, err := s.waitForAgents(ctx, sc.AgentName, sc.Namespace, false, failedCreateCh)
	if err != nil {
		// If no agent arrives, then drop its entry from the configmap. This ensures that there
		// are no false positives the next time an intercept is attempted.
		s.dropAgentConfig(parentCtx, wl)
		return nil, nil, err
	}
	sortAgents(as)
	return sc, as, nil
}

// waitForNodeAgent waits for the node-agent Job for the agent named name in
// namespace (created during PrepareIntercept, or by ensureNodeAgent for an
// ingest EnsureAgent call) to register itself as an agent, without injecting
// a sidecar. It mirrors the wait ensureAgent performs for the sidecar path,
// but skips config persistence and pod eviction, since a node-agent is a
// standalone Job rather than an injected container. The caller is
// responsible for having already provisioned the Job.
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
	return s.waitForAgents(ctx, name, namespace, true, failedCreateCh)
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

func (s *State) createAgentConfig(ctx context.Context, wl k8sapi.Workload, agentImage string) (*agentconfig.Sidecar, error) {
	gc, err := managerutil.GetEnv(ctx).GeneratorConfig(agentImage)
	if err != nil {
		return nil, err
	}
	clog.Debugf(ctx, "generating new agent config for %s", wl)
	sc, err := gc.Generate(ctx, wl, nil)
	if err != nil {
		return nil, err
	}
	if err = s.ValidateCreateAgent(ctx, wl, sc); err != nil {
		return nil, err
	}
	return sc, nil
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

func (s *State) waitForAgents(
	ctx context.Context,
	name, namespace string,
	nodeAgent bool,
	failedCreateCh <-chan *events.Event,
) ([]*AgentSession, error) {
	clog.Debugf(ctx, "Waiting for agent %s.%s", name, namespace)
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
			upserts := delta.Upserts
			if len(upserts) == 0 {
				continue
			}
			as := make([]*AgentSession, 0)
			for _, a := range upserts {
				if mm.IsInactive(k8sTypes.UID(a.PodUid)) {
					clog.Debugf(ctx, "Agent %s(%s) is blacklisted", a.PodName, a.PodIp)
				} else {
					clog.Debugf(ctx, "Agent %s(%s) is ready", a.PodName, a.PodIp)
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
			return nil, errcat.User.New(bf.String())
		}
	}
}
