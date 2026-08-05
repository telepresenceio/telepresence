package trafficmgr

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type ingestKey struct {
	workload  string
	container string
	namespace string
}

func (ik ingestKey) String() string {
	return fmt.Sprintf("%s.%s[%s]", ik.workload, ik.namespace, ik.container)
}

type ingest struct {
	*manager.AgentInfo
	ingestKey
	wg               sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
	localMountPoint  string
	localMountPort   int32
	localPorts       []string
	handlerContainer string
	pid              int
	mounter          remotefs.Mounter
}

func (ig *ingest) podAccess(rd daemon.DaemonClient) *podAccess {
	ni := ig.Containers[ig.container]
	pa := &podAccess{
		ctx:              ig.ctx,
		localPorts:       ig.localPorts,
		workload:         ig.workload,
		namespace:        ig.Namespace,
		container:        ig.container,
		podIP:            ig.PodIp,
		sftpPort:         ig.SftpPort,
		ftpPort:          ig.FtpPort,
		mountPoint:       ni.MountPoint,
		clientMountPoint: ig.localMountPoint,
		localMountPort:   ig.localMountPort,
		mounter:          &ig.mounter,
		readOnly:         true,
		wg:               &ig.wg,
	}
	if err := pa.ensureAccess(ig.ctx, rd); err != nil {
		clog.Error(ig.ctx, err)
	}
	return pa
}

func (ig *ingest) response() *rpc.IngestInfo {
	cn := ig.Containers[ig.container]
	ii := &rpc.IngestInfo{
		Workload:         ig.workload,
		WorkloadKind:     ig.Kind,
		Container:        ig.container,
		Namespace:        ig.namespace,
		PodIp:            ig.PodIp,
		SftpPort:         ig.SftpPort,
		FtpPort:          ig.FtpPort,
		MountPoint:       cn.MountPoint,
		Mounts:           cn.Mounts,
		ClientMountPoint: ig.localMountPoint,
		Environment:      cn.Environment,
	}
	if ig.handlerContainer != "" {
		if ii.Environment == nil {
			ii.Environment = make(map[string]string, 1)
		}
		ii.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"] = ig.handlerContainer
	}
	return ii
}

func (s *session) getSingleContainerName(ai *manager.AgentInfo) (name string, err error) {
	if err = s.validateAgentForIngest(ai); err != nil {
		return "", err
	}
	if len(ai.Containers) > 1 {
		return "", status.Error(codes.NotFound, fmt.Sprintf("workload %s has multiple containers. Please specify which one to use", ai.Name))
	}
	for name = range ai.Containers {
	}
	return name, err
}

func (s *session) validateAgentForIngest(ai *manager.AgentInfo) error {
	if len(ai.Containers) == 0 {
		return status.Error(codes.Unimplemented, fmt.Sprintf("traffic-manager %s has no support for ingest", s.ManagerVersion()))
	}
	return nil
}

// getCurrentAgent returns the cached agentPod matching the workload name in the
// given namespace, or nil when no such agent is cached. Consulted only for the
// ingest fast path and node-agent semantics; container-level data always comes
// from EnsureAgent.
func (s *session) getCurrentAgent(workload, namespace string) *agentPod {
	for _, ap := range s.getCurrentAgentPods() {
		if ap.workload == workload && ap.namespace == namespace {
			found := ap
			return &found
		}
	}
	return nil
}

func (s *session) Ingest(ctx context.Context, rq *rpc.IngestRequest) (ir *rpc.IngestInfo, err error) {
	if err = requireAgentPortForward(ctx, "ingest"); err != nil {
		return nil, err
	}
	if rq.NodeAgent && s.compareFinalizedManagerVersion(2, 30, 0) < 0 {
		return nil, errcat.User.Newf("traffic-manager version %s has no support for node-agents", s.ManagerVersion())
	}
	id := rq.Identifier
	ns := id.Namespace
	if ns == "" {
		ns = s.Namespace
	} else if validated := s.ActualNamespace(ns); validated == "" {
		return nil, errcat.User.Newf(
			"namespace %q is not mapped or is not accessible. Reconnect with --mapped-namespaces including %q, and verify your access to that namespace",
			ns, ns)
	} else {
		ns = validated
	}
	ik := ingestKey{
		workload:  id.WorkloadName,
		container: id.ContainerName,
		namespace: ns,
	}

	// Fast path: an ingest already exists for this key -- or, when the
	// container is unspecified, exactly one ingest exists for this
	// workload+namespace.
	if ik.container != "" {
		if ig, loaded := s.currentIngests.Load(ik); loaded {
			return ig.response(), nil
		}
	} else {
		var found *ingest
		s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
			if key.workload != ik.workload || key.namespace != ik.namespace {
				return true
			}
			if found != nil {
				err = status.Error(codes.NotFound, fmt.Sprintf("workload %s has multiple ingests. Please specify which one to use", ik.workload))
				return false
			}
			found = ig
			return true
		})
		if err != nil {
			return nil, err
		}
		if found != nil {
			return found.response(), nil
		}
	}

	// Node-agent semantics via the pod cache: existence and node-agent flag
	// only, container-level data is never cached (see the agentPod comment).
	ap := s.getCurrentAgent(ik.workload, ik.namespace)
	if rq.NodeAgent && ap != nil && !ap.nodeAgent {
		return nil, errcat.User.Newf(
			"workload %s already has an injected traffic-agent, which a node-agent cannot replace; "+
				"omit --node-agent, or uninstall the existing agent and retry",
			ik.workload)
	}
	// A node-agent serves env and mounts identically to a sidecar, so a
	// cached node-agent silently satisfies a plain (non-node-agent) ingest
	// request too. Requesting NodeAgent from EnsureAgent in that case
	// prevents the manager from injecting a sidecar on top of the live
	// node-agent.
	nodeAgent := rq.NodeAgent || (ap != nil && ap.nodeAgent)

	err = s.ensureNoMountConflict(rq.MountPoint, rq.LocalMountPort)
	if err != nil {
		return nil, err
	}

	var as *manager.AgentInfoSnapshot
	timeoutCtx, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutIntercept)
	defer cancel()
	as, err = s.ManagerClient().EnsureAgent(timeoutCtx, &manager.EnsureAgentRequest{
		Session:   s.sessionInfo,
		Name:      ik.workload,
		Namespace: ik.namespace,
		NodeAgent: nodeAgent,
	})
	if err != nil {
		return nil, err
	}
	ai := as.Agents[0]
	// A sidecar returned when a node-agent was explicitly requested must
	// never be used silently: its env/mounts assume no sidecar was
	// injected, so a mismatch here means the traffic-manager did not
	// honor node-agent mode (e.g. the version gate above was bypassed by
	// a manager that predates the node_agent field). Fail loudly instead
	// of quietly falling back to sidecar semantics.
	if rq.NodeAgent && !ai.NodeAgent {
		return nil, errcat.User.Newf(
			"traffic-manager did not honor node-agent mode for workload %s", ik.workload)
	}
	if err = s.validateAgentForIngest(ai); err != nil {
		return nil, err
	}

	if ik.container == "" {
		ik.container, err = s.getSingleContainerName(ai)
		if err != nil {
			return nil, err
		}
	} else if _, ok := ai.Containers[ik.container]; !ok {
		return nil, fmt.Errorf("workload %s has no container named %s", ik.workload, ik.container)
	}

	err = s.translateContainerEnv(ctx, ai, ik.container)
	if err != nil {
		return nil, err
	}

	ig, loaded := s.currentIngests.LoadOrCompute(ik, func() (*ingest, bool) {
		ctx, cancel := context.WithCancel(s)
		cancelIngest := func() {
			s.currentIngests.Delete(ik)
			clog.Debugf(ctx, "Cancelling ingest %s", ik)
			cancel()
			s.ingestTracker.cancelContainer(ik.workload, ik.container)
		}
		return &ingest{
			ingestKey:       ik,
			AgentInfo:       ai,
			ctx:             ctx,
			cancel:          cancelIngest,
			localMountPoint: rq.MountPoint,
			localMountPort:  rq.LocalMountPort,
			localPorts:      rq.LocalPorts,
		}, false
	})
	if !loaded {
		s.startIngestPodAccess(ctx, ig, true)
	}
	return ig.response(), nil
}

func (s *session) startIngestPodAccess(ctx context.Context, ig *ingest, initial bool) {
	err := s.WithRootClient(ctx, func(_ context.Context, rd daemon.DaemonClient) error {
		if initial {
			s.ingestTracker.initialStart(ig.podAccess(rd))
		} else {
			s.ingestTracker.start(ig.podAccess(rd))
		}
		return nil
	})
	if err != nil {
		clog.Errorf(ctx, "failed to start ingest pod access: %v", err)
	}
}

func (s *session) translateContainerEnv(ctx context.Context, ai *manager.AgentInfo, container string) error {
	cn, ok := ai.Containers[container]
	if !ok {
		return fmt.Errorf("workload %s has no container named %s", ai.Name, container)
	}
	return s.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
		env, err := rd.TranslateEnvIPs(ctx, &daemon.Environment{Env: cn.Environment})
		if err == nil {
			cn.Environment = env.Env
		}
		return err
	})
}

func (s *session) getCurrentIngests() []*rpc.IngestInfo {
	ingests := make([]*rpc.IngestInfo, 0, s.currentIngests.Size())
	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		ingests = append(ingests, ig.response())
		return true
	})
	return ingests
}

// findIngest returns the ingest matching the given (workload, container, namespace) tuple.
// Empty containerName or namespace is treated as "any" — the ingest is resolved by scanning
// currentIngests, and an error is returned if the remaining filters match more than one ingest.
func (s *session) findIngest(workloadName, containerName, namespace string) (ig *ingest, err error) {
	if containerName == "" || namespace == "" {
		var foundIngest *ingest
		var err error

		s.currentIngests.Range(func(key ingestKey, value *ingest) bool {
			if key.workload != workloadName {
				return true
			}
			if containerName != "" && key.container != containerName {
				return true
			}
			if namespace != "" && key.namespace != namespace {
				return true
			}
			if foundIngest != nil {
				err = status.Error(codes.NotFound, fmt.Sprintf("workload %s has multiple ingests. Please specify which one to use", workloadName))
				return false
			}
			foundIngest = value
			return true
		})
		if err != nil {
			return nil, err
		}

		if foundIngest == nil {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("no ingest found for workload %s", workloadName))
		}

		return foundIngest, nil
	}

	ik := ingestKey{
		workload:  workloadName,
		container: containerName,
		namespace: namespace,
	}
	if ig, ok := s.currentIngests.Load(ik); ok {
		return ig, nil
	}
	return nil, status.Error(codes.NotFound, fmt.Sprintf("ingest %s doesn't exist", ik))
}

func (s *session) getIngest(rq *rpc.IngestIdentifier) (ig *ingest, err error) {
	return s.findIngest(rq.WorkloadName, rq.ContainerName, rq.Namespace)
}

func (s *session) GetIngest(rq *rpc.IngestIdentifier) (ii *rpc.IngestInfo, err error) {
	ig, err := s.getIngest(rq)
	if err != nil {
		return nil, err
	}
	return ig.response(), nil
}

func (s *session) LeaveIngest(rq *rpc.IngestIdentifier) (ii *rpc.IngestInfo, err error) {
	ig, err := s.getIngest(rq)
	if err != nil {
		return nil, err
	}
	s.stopHandler(fmt.Sprintf("%s/%s/%s", ig.workload, ig.container, ig.namespace), ig.handlerContainer, ig.pid)
	ig.cancel()
	ig.wg.Wait()
	s.releaseNodeAgentIfLast(ig)
	return ig.response(), nil
}

// releaseNodeAgentIfLast tells the traffic-manager to drop this session's claim on ig's
// node-agent Job once ig was the last ingest referencing its {workload, namespace}. It is
// called from LeaveIngest, the only path that explicitly ends a single ingest while the
// session stays up; a full session teardown (ClearIngestsAndIntercepts) does not need this
// call because the traffic-manager already drops every lease held by a session that
// disconnects (session-end lease GC), so nothing would be gained by a per-ingest RPC that
// might delay shutdown.
//
// ig.cancel() has already removed ig from s.currentIngests by the time this runs, so a Range
// that finds no other entry for the same {workload, namespace} means ig was the last one.
// The call is best-effort: the manager's reconciler reaps orphaned node-agent Jobs on its
// own, so a failed or skipped release must never fail the leave.
func (s *session) releaseNodeAgentIfLast(ig *ingest) {
	if ig.AgentInfo == nil || !ig.NodeAgent {
		return
	}
	stillClaimed := false
	s.currentIngests.Range(func(_ ingestKey, other *ingest) bool {
		if other.workload == ig.workload && other.namespace == ig.namespace {
			stillClaimed = true
			return false
		}
		return true
	})
	if stillClaimed {
		return
	}
	ctx, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficManagerAPI)
	defer cancel()
	if _, err := s.ManagerClient().ReleaseAgent(ctx, &manager.ReleaseAgentRequest{
		Session:   s.sessionInfo,
		Name:      ig.workload,
		Namespace: ig.namespace,
	}); err != nil {
		clog.Warnf(ctx, "failed to release node-agent for workload %s.%s: %v", ig.workload, ig.namespace, err)
	}
}
