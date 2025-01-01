package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

// intercept tracks the life-cycle of an intercept, dictated by the intercepts
// arrival and departure in the watchInterceptsLoop.
type intercept struct {
	sync.Mutex
	*manager.InterceptInfo

	// ctx is a context cancelled by the cancel attribute. It must be used by
	// services that should be cancelled when the intercept ends
	ctx context.Context

	// cancel is called when the intercept is no longer present
	cancel context.CancelFunc

	// wg is the group to wait for after a call to cancel
	wg sync.WaitGroup

	// pid of intercept handler for an intercept. This entry will only be present when
	// the telepresence intercept command spawns a new command. The int value reflects
	// the pid of that new command.
	pid int

	// handlerContainer is the name or ID of the container that the intercept handler is
	// running in, when it runs in Docker. As with pid, this entry will only be present when
	// the telepresence intercept command spawns a new command using --docker-run or
	// --docker-build
	handlerContainer string

	// The mounter of the remote file system.
	remotefs.Mounter

	// Use bridged ftp/sftp mount through this local port
	localMountPort int32

	// Mount read-only
	readOnly bool
}

// interceptResult is what gets written to the awaitIntercept's waitCh channel when the
// awaited intercept arrives.
type interceptResult struct {
	intercept  *intercept
	mountsDone <-chan struct{}
	err        error
}

// awaitIntercept is what the traffic-manager is using to notify the watchInterceptsLoop
// about an expected intercept arrival.
type awaitIntercept struct {
	// mountPoint is the mount point assigned to the InterceptInfo's ClientMountPoint when
	// it arrives from the traffic-manager.
	mountPoint string

	// mountPort is optional and indicates that a TCP bridge should be established, allowing
	// the mount to take place in a host
	mountPort int32

	readOnly bool
	waitCh   chan<- interceptResult
}

func (ic *intercept) localPorts() []string {
	// Older versions use ii.extraPorts (TCP only), newer versions use ii.localPorts.
	ps := ic.Spec.LocalPorts
	if len(ps) == 0 {
		for _, ep := range ic.Spec.ExtraPorts {
			ps = append(ps, strconv.Itoa(int(ep)))
		}
		ic.Spec.LocalPorts = ps
	}
	return ps
}

func (ic *intercept) podAccess(rd daemon.DaemonClient) *podAccess {
	pa := &podAccess{
		ctx:              ic.ctx,
		localPorts:       ic.localPorts(),
		workload:         ic.Spec.Agent,
		podIP:            ic.PodIp,
		container:        ic.Spec.ContainerName,
		sftpPort:         ic.SftpPort,
		ftpPort:          ic.FtpPort,
		mountPoint:       ic.MountPoint,
		clientMountPoint: ic.ClientMountPoint,
		localMountPort:   ic.localMountPort,
		readOnly:         ic.readOnly,
		mounter:          &ic.Mounter,
	}
	if err := pa.ensureAccess(ic.ctx, rd); err != nil {
		dlog.Error(ic.ctx, err)
	}
	return pa
}

func (s *session) watchInterceptsHandler(ctx context.Context) error {
	// Don't use a dgroup.Group because:
	//  1. we don't actually care about tracking errors (we just always retry) or any of
	//     dgroup's other functionality
	//  2. because goroutines may churn as intercepts are created and deleted, tracking all of
	//     their exit statuses is just a memory leak
	//  3. because we want a per-worker cancel, we'd have to implement our own Context
	//     management on top anyway, so dgroup wouldn't actually save us any complexity.
	return runWithRetry(ctx, s.watchInterceptsLoop)
}

func (s *session) watchInterceptsLoop(ctx context.Context) error {
	stream, err := s.managerClient.WatchIntercepts(ctx, s.SessionInfo())
	if err != nil {
		return fmt.Errorf("manager.WatchIntercepts dial: %w", err)
	}
	pat := newPodAccessTracker()
	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are cancelled correctly.
			s.handleInterceptSnapshot(ctx, pat, nil)
			if ctx.Err() != nil || errors.Is(err, io.EOF) || grpcStatus.Code(err) == grpcCodes.NotFound {
				// Normal termination
				return nil
			}
			return fmt.Errorf("manager.WatchIntercepts recv: %w", err)
		}
		s.handleInterceptSnapshot(ctx, pat, snapshot.Intercepts)
	}
	return nil
}

func (s *session) handleInterceptSnapshot(ctx context.Context, pat *podAccessTracker, intercepts []*manager.InterceptInfo) {
	s.setCurrentIntercepts(ctx, intercepts)
	pat.initSnapshot()

	for _, ii := range intercepts {
		if ii.Disposition == manager.InterceptDispositionType_WAITING {
			continue
		}

		s.currentInterceptsLock.Lock()
		ic := s.currentIntercepts[ii.Id]
		aw := s.interceptWaiters[ii.Spec.Name]
		if aw != nil {
			delete(s.interceptWaiters, ii.Spec.Name)
		}
		s.currentInterceptsLock.Unlock()

		var err error
		if ii.Disposition == manager.InterceptDispositionType_ACTIVE {
			ns := ii.Spec.Namespace
			if s.Namespace != ns {
				err = errcat.User.Newf("active intercepts in both namespace %s and %s", ns, s.Namespace)
			}
		} else {
			err = fmt.Errorf("intercept in error state %v: %v", ii.Disposition, ii.Message)
		}

		// Notify waiters for active intercepts
		pa := ic.podAccess(s.rootDaemon)
		if aw != nil {
			dlog.Debugf(ctx, "wait status: intercept id=%q is no longer WAITING; is now %v", ii.Id, ii.Disposition)
			ir := interceptResult{
				intercept:  ic,
				err:        err,
				mountsDone: pat.getOrCreateMountsDone(pa),
			}
			select {
			case aw.waitCh <- ir:
				if err != nil {
					// Error logged by receiver
					continue
				}
			default:
				// Channel was closed
				dlog.Debugf(ctx, "unable to propagate intercept id=%q", ii.Id)
			}
		}
		if err != nil {
			dlog.Error(ctx, err)
			continue
		}

		if s.isPodDaemon {
			// disable mount point logic
			pa.ftpPort = 0
			pa.sftpPort = 0
		}
		pat.start(pa)
	}
	pat.cancelUnwanted(ctx)
}

// getCurrentIntercepts returns a copy of the current intercept snapshot. This snapshot does
// not include any local-only intercepts.
func (s *session) getCurrentIntercepts() []*intercept {
	// Copy the current snapshot
	s.currentInterceptsLock.Lock()
	intercepts := maps.ToSortedSlice(s.currentIntercepts)
	s.currentInterceptsLock.Unlock()
	return intercepts
}

// getCurrentInterceptInfos returns the InterceptInfos of the current intercept snapshot.
func (s *session) getCurrentInterceptInfos() []*manager.InterceptInfo {
	// Copy the current snapshot
	ics := s.getCurrentIntercepts()
	ifs := make([]*manager.InterceptInfo, len(ics))
	for idx, ic := range ics {
		ifs[idx] = ic.InterceptInfo
	}
	return ifs
}

func (s *session) setCurrentIntercepts(ctx context.Context, iis []*manager.InterceptInfo) {
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	intercepts := make(map[string]*intercept, len(iis))
	sb := strings.Builder{}
	sb.WriteByte('[')
	for i, ii := range iis {
		ic, ok := s.currentIntercepts[ii.Id]
		if ok {
			// retain ClientMountPoint, it's assigned in the client and never passed from the traffic-manager
			ii.ClientMountPoint = ic.ClientMountPoint
			ic.InterceptInfo = ii
		} else {
			ic = &intercept{InterceptInfo: ii}
			ic.ctx, ic.cancel = context.WithCancel(ctx)
			dlog.Debugf(ctx, "Received new intercept %s", ic.Spec.Name)
			if aw, ok := s.interceptWaiters[ii.Spec.Name]; ok {
				ic.ClientMountPoint = aw.mountPoint
				ic.localMountPort = aw.mountPort
				ic.readOnly = aw.readOnly
			}
		}
		intercepts[ii.Id] = ic
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(ii.Spec.Name)
		sb.WriteByte('=')
		sb.WriteString(ii.PodIp)
	}
	sb.WriteByte(']')
	dlog.Debugf(ctx, "setCurrentIntercepts(%s)", sb.String())

	// Cancel those that no longer exists
	for id, ic := range s.currentIntercepts {
		if _, ok := intercepts[id]; !ok {
			dlog.Debugf(ctx, "Cancelling context for intercept %s", ic.Spec.Name)
			ic.cancel()
		}
	}
	s.currentIntercepts = intercepts
	s.reconcileAPIServers(ctx)
}

func InterceptError(tp common.InterceptError, err error) *rpc.InterceptResult {
	return &rpc.InterceptResult{
		Error:         tp,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

type interceptInfo struct {
	// Information provided by the traffic manager as response to the PrepareIntercept call
	preparedIntercept *manager.PreparedIntercept
}

func (s *interceptInfo) InterceptResult() *rpc.InterceptResult {
	pi := s.preparedIntercept
	return &rpc.InterceptResult{
		ServiceUid:   pi.ServiceUid,
		WorkloadKind: pi.WorkloadKind,
	}
}

func (s *interceptInfo) PortIdentifier() (agentconfig.PortIdentifier, error) {
	var spi string
	if s.preparedIntercept.ServicePortName == "" {
		spi = strconv.Itoa(int(s.preparedIntercept.ServicePort))
	} else {
		spi = s.preparedIntercept.ServicePortName
	}
	return agentconfig.NewPortIdentifier(s.preparedIntercept.Protocol, spi)
}

func (s *interceptInfo) PreparedIntercept() *manager.PreparedIntercept {
	return s.preparedIntercept
}

func (s *session) ensureNoInterceptConflict(ir *rpc.CreateInterceptRequest) *rpc.InterceptResult {
	err := s.ensureNoMountConflict(ir.MountPoint, ir.LocalMountPort)
	if err != nil {
		return &rpc.InterceptResult{
			Error:         common.InterceptError_MOUNT_POINT_BUSY,
			ErrorText:     err.Error(),
			ErrorCategory: int32(errcat.User),
		}
	}
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	spec := ir.Spec
	for _, iCept := range s.currentIntercepts {
		if iCept.Spec.Name == spec.Name {
			return InterceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.New(spec.Name))
		}
	}
	return nil
}

// allBusyLocalPorts returns the sum of all ports that the intercept forwards to and all ports
// that are forwarded from.
func allBusyLocalPorts(spec *manager.InterceptSpec) []agentconfig.PortAndProto {
	targetPort := spec.TargetPort
	if targetPort == 0 {
		targetPort = spec.ContainerPort
	}
	ports := make([]agentconfig.PortAndProto, 0, len(spec.LocalPorts)+len(spec.PodPorts)+1)
	ports = append(ports, agentconfig.PortAndProto{
		Port:  uint16(targetPort),
		Proto: core.Protocol(spec.Protocol),
	})
	for _, lp := range spec.LocalPorts {
		pp, _ := agentconfig.NewPortAndProto(lp)
		ports = append(ports, pp)
	}
	for _, ps := range spec.PodPorts {
		pm := agentconfig.PortMapping(ps)
		ports = append(ports, pm.To())
	}
	return ports
}

// ensureUniqueLocalPorts returns the sum of all local ports that the intercept will forward to, and all
// local ports that the client will forward from. Also ensures that there are no conflicts among those ports.
// The cluster-side of the port mappings are not checked here because we rely on the PrepareIntercept
// call to already have done that.
func ensureUniqueLocalPorts(spec *manager.InterceptSpec, pi *manager.PreparedIntercept) (map[agentconfig.PortAndProto]struct{}, error) {
	targetPort := spec.TargetPort
	if targetPort == 0 {
		targetPort = pi.ContainerPort
	}

	ports := make(map[agentconfig.PortAndProto]struct{}, len(spec.LocalPorts)+len(spec.PodPorts)+1)
	ports[agentconfig.PortAndProto{
		Port:  uint16(targetPort),
		Proto: core.Protocol(pi.Protocol),
	}] = struct{}{}

	for _, lp := range spec.LocalPorts {
		pp, err := agentconfig.NewPortAndProto(lp)
		if err != nil {
			return nil, err
		}
		if _, ok := ports[pp]; ok {
			return nil, fmt.Errorf("multiple use of port %s on %s", pp, spec.TargetHost)
		}
		ports[pp] = struct{}{}
	}
	for _, ps := range pi.PodPorts {
		pm := agentconfig.PortMapping(ps)
		if err := pm.Validate(); err != nil {
			return nil, err
		}
		pp := pm.To()
		if _, ok := ports[pp]; ok {
			return nil, fmt.Errorf("multiple use of port %s on %s", pp, spec.TargetHost)
		}
		ports[pp] = struct{}{}
	}
	return ports, nil
}

func (s *session) ensureNoPortConflict(spec *manager.InterceptSpec, ir *manager.PreparedIntercept) *rpc.InterceptResult {
	ports, err := ensureUniqueLocalPorts(spec, ir)
	if err != nil {
		return InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.User.New(err))
	}

	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	for _, ci := range s.currentIntercepts {
		ciSpec := ci.Spec
		for _, blp := range allBusyLocalPorts(ciSpec) {
			if _, ok := ports[blp]; ok {
				return &rpc.InterceptResult{
					Error:         common.InterceptError_LOCAL_TARGET_IN_USE,
					ErrorText:     fmt.Sprintf("Port %s is already in use by intercept %s", net.JoinHostPort(ciSpec.TargetHost, blp.String()), ciSpec.Name),
					ErrorCategory: int32(errcat.User),
				}
			}
		}
	}
	return nil
}

// CanIntercept checks if it is possible to create an intercept for the given request. The intercept can proceed
// only if the returned rpc.InterceptResult is nil. The returned runtime.Object is either nil, indicating a local
// intercept, or the workload for the intercept.
func (s *session) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (userd.InterceptInfo, *rpc.InterceptResult) {
	spec := ir.Spec
	if spec.Namespace == "" {
		spec.Namespace = s.Namespace
	} else if s.Namespace != spec.Namespace {
		return nil, InterceptError(common.InterceptError_NAMESPACE_AMBIGUITY, errcat.User.Newf("%s,%s", s.Namespace, spec.Namespace))
	}

	self := s.self
	if er := s.ensureNoInterceptConflict(ir); er != nil {
		return nil, er
	}
	if spec.Agent == "" {
		return nil, nil
	}

	mgrIr := self.NewCreateInterceptRequest(spec)
	if er := self.InterceptProlog(c, mgrIr); er != nil {
		return nil, er
	}
	pi, err := s.managerClient.PrepareIntercept(c, mgrIr)
	if err != nil {
		if st, ok := grpcStatus.FromError(err); ok {
			if st.Code() == grpcCodes.FailedPrecondition {
				return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.User.New(st.Message()))
			}
		}
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	if pi.Error != "" {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.Category(pi.ErrorCategory).New(pi.Error))
	}
	if er := s.ensureNoPortConflict(spec, pi); er != nil {
		return nil, er
	}

	iInfo := &interceptInfo{preparedIntercept: pi}
	return iInfo, nil
}

func (s *session) NewCreateInterceptRequest(spec *manager.InterceptSpec) *manager.CreateInterceptRequest {
	return &manager.CreateInterceptRequest{
		Session:       s.self.SessionInfo(),
		InterceptSpec: spec,
	}
}

// AddIntercept adds one intercept.
func (s *session) AddIntercept(c context.Context, ir *rpc.CreateInterceptRequest) *rpc.InterceptResult {
	self := s.self
	iInfo, result := self.CanIntercept(c, ir)
	if result != nil {
		return result
	}

	spec := ir.Spec
	if iInfo == nil {
		return &rpc.InterceptResult{Error: common.InterceptError_UNSPECIFIED}
	}

	spec.Client = s.clientID
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	mgrClient := self.ManagerClient()

	// iInfo.preparedIntercept == nil means that we're using an older traffic-manager, incapable
	// of using PrepareIntercept.
	pi := iInfo.PreparedIntercept()

	if pi.ServicePort > 0 || pi.ServicePortName != "" {
		// Make spec port identifier unambiguous.
		spec.ServicePortName = pi.ServicePortName
		spec.ServicePort = pi.ServicePort
		pti, err := iInfo.PortIdentifier()
		if err != nil {
			return InterceptError(common.InterceptError_MISCONFIGURED_WORKLOAD, err)
		}
		spec.PortIdentifier = pti.String()
	}
	dlog.Debugf(c, "pi.Protocol = %s", pi.Protocol)
	spec.Protocol = pi.Protocol
	spec.ContainerPort = pi.ContainerPort
	spec.PodPorts = pi.PodPorts
	result = iInfo.InterceptResult()

	if spec.TargetPort == 0 {
		spec.TargetPort = pi.ContainerPort
	}

	spec.ServiceUid = result.ServiceUid
	spec.WorkloadKind = result.WorkloadKind

	dlog.Debugf(c, "creating intercept %s", spec.Name)
	tos := client.GetConfig(c).Timeouts()
	spec.RoundtripLatency = int64(tos.Get(client.TimeoutRoundtripLatency)) * 2 // Account for extra hop
	spec.DialTimeout = int64(tos.Get(client.TimeoutEndpointDial))
	c, cancel := tos.TimeoutContext(c, client.TimeoutIntercept)
	defer cancel()

	// The agent is in place and the traffic-manager has acknowledged the creation of the intercept. It
	// should become active within a few seconds.
	waitCh := make(chan interceptResult, 2) // Need a buffer because reply can come before we're reading the channel,
	s.currentInterceptsLock.Lock()
	s.interceptWaiters[spec.Name] = &awaitIntercept{
		mountPoint: ir.MountPoint,
		mountPort:  ir.LocalMountPort,
		readOnly:   ir.MountReadOnly,
		waitCh:     waitCh,
	}
	s.currentInterceptsLock.Unlock()
	defer func() {
		s.currentInterceptsLock.Lock()
		if _, ok := s.interceptWaiters[spec.Name]; ok {
			delete(s.interceptWaiters, spec.Name)
			close(waitCh)
		}
		s.currentInterceptsLock.Unlock()
	}()

	ii, err := mgrClient.CreateIntercept(c, self.NewCreateInterceptRequest(spec))
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		return InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}

	dlog.Debugf(c, "created intercept %s", ii.Spec.Name)

	success := false
	defer func() {
		if !success {
			dlog.Debugf(c, "intercept %s failed to create, will remove...", ii.Spec.Name)

			// Make an attempt to remove the created intercept using a time limited Context. Our
			// context is already done.
			rc, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
			defer cancel()
			if removeErr := self.RemoveIntercept(rc, ii.Spec.Name); removeErr != nil {
				dlog.Warnf(c, "failed to remove failed intercept %s: %v", ii.Spec.Name, removeErr)
			}
		}
	}()

	// Wait for the intercept to transition from WAITING or NO_AGENT to ACTIVE. This
	// might result in more than one event.
	for {
		select {
		case <-c.Done():
			return InterceptError(common.InterceptError_FAILED_TO_ESTABLISH, client.CheckTimeout(c, c.Err()))
		case wr := <-waitCh:
			if wr.err != nil {
				return InterceptError(common.InterceptError_FAILED_TO_ESTABLISH, wr.err)
			}
			ic := wr.intercept
			ii = ic.InterceptInfo
			if ii.Disposition != manager.InterceptDispositionType_ACTIVE {
				continue
			}
			result.InterceptInfo = ii
			select {
			case <-c.Done():
				return InterceptError(common.InterceptError_FAILED_TO_ESTABLISH, client.CheckTimeout(c, c.Err()))
			case <-wr.mountsDone:
			}

			if er := self.InterceptEpilog(c, ir, result); er != nil {
				return er
			}
			env, err := s.rootDaemon.TranslateEnvIPs(c, &daemon.Environment{Env: result.InterceptInfo.Environment})
			if err != nil {
				return InterceptError(common.InterceptError_INTERNAL, client.CheckTimeout(c, err))
			}
			result.InterceptInfo.Environment = env.Env
			success = true // Prevent removal in deferred function
			return result
		}
	}
}

func (s *session) InterceptProlog(context.Context, *manager.CreateInterceptRequest) *rpc.InterceptResult {
	return nil
}

func (s *session) InterceptEpilog(context.Context, *rpc.CreateInterceptRequest, *rpc.InterceptResult) *rpc.InterceptResult {
	return nil
}

// RemoveIntercept removes one intercept by name.
func (s *session) RemoveIntercept(c context.Context, name string) error {
	dlog.Debugf(c, "Removing intercept %s", name)
	ii := s.getInterceptByName(name)
	if ii == nil {
		dlog.Debugf(c, "Intercept %s was already removed", name)
		return nil
	}
	return s.removeIntercept(c, ii)
}

func (s *session) removeIntercept(c context.Context, ic *intercept) error {
	name := ic.Spec.Name
	s.stopHandler(c, name, ic.handlerContainer, ic.pid)

	// Unmount filesystems before telling the manager to remove the intercept
	ic.cancel()
	ic.wg.Wait()

	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	c, cancel := client.GetConfig(c).Timeouts().TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()
	_, err := s.managerClient.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: s.SessionInfo(),
		Name:    name,
	})
	return err
}

func (s *session) stopHandler(c context.Context, name, handlerContainer string, pid int) {
	// No use trying to kill processes when using a container-based daemon, unless
	// that daemon runs as a normal user daemon with a separate root daemon.
	// Some users run a standard telepresence client together with ingests/intercepts
	// in one single container.
	if !(proc.RunningInContainer() && userd.GetService(c).RootSessionInProcess()) {
		if handlerContainer != "" {
			if err := docker.StopContainer(docker.EnableClient(c), handlerContainer); err != nil {
				dlog.Error(c, err)
			}
		} else if pid != 0 {
			p, err := os.FindProcess(pid)
			if err != nil {
				dlog.Errorf(c, "unable to find handler for ingest/intercept %s with pid %d", name, pid)
			} else {
				dlog.Debugf(c, "terminating interceptor for ingest/intercept %s with pid %d", name, pid)
				_ = proc.Terminate(p)
			}
		}
	}
}

// AddInterceptor associates the given intercept with a running process. This ensures that
// the running process will be signalled when the intercept is removed.
func (s *session) AddInterceptor(ctx context.Context, id string, ih *rpc.Interceptor) error {
	added := false
	s.currentInterceptsLock.Lock()
	if ci, ok := s.currentIntercepts[id]; ok {
		dlog.Debugf(ctx, "Adding intercept handler for id %s, %v", id, ih)
		ci.pid = int(ih.Pid)
		ci.handlerContainer = ih.ContainerName
		added = true
	} else {
		if parts := strings.Split(id, "/"); len(parts) == 2 {
			if cg, ok := s.currentIngests.Load(ingestKey{workload: parts[0], container: parts[1]}); ok {
				dlog.Debugf(ctx, "Adding ingest handler for id %s, %v", id, ih)
				cg.pid = int(ih.Pid)
				cg.handlerContainer = ih.ContainerName
				added = true
			}
		}
	}
	s.currentInterceptsLock.Unlock()
	if !added {
		dlog.Warnf(ctx, "Found no ingest or intercept handler for id %s, %v", id, ih)
	}
	return nil
}

func (s *session) RemoveInterceptor(id string) error {
	s.currentInterceptsLock.Lock()
	if ci, ok := s.currentIntercepts[id]; ok {
		ci.pid = 0
		ci.handlerContainer = ""
	} else {
		if parts := strings.Split(id, "/"); len(parts) == 2 {
			if cg, ok := s.currentIngests.Load(ingestKey{workload: parts[0], container: parts[1]}); ok {
				cg.pid = 0
				cg.handlerContainer = ""
			}
		}
	}
	s.currentInterceptsLock.Unlock()
	return nil
}

// GetInterceptSpec returns the InterceptSpec for the given name, or nil if no such spec exists.
func (s *session) GetInterceptSpec(name string) *manager.InterceptSpec {
	if ic := s.getInterceptByName(name); ic != nil {
		return ic.Spec
	}
	return nil
}

// GetInterceptInfo returns the InterceptInfo for the given name, or nil if no such info exists.
func (s *session) GetInterceptInfo(name string) *manager.InterceptInfo {
	if ic := s.getInterceptByName(name); ic != nil {
		ii := ic.InterceptInfo
		if ic.handlerContainer != "" {
			if ii.Environment == nil {
				ii.Environment = make(map[string]string, 1)
			}
			ii.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"] = ic.handlerContainer
		}
		return ii
	}
	return nil
}

// GetInterceptSpec returns the InterceptSpec for the given name, or nil if no such spec exists.
func (s *session) getInterceptByName(name string) (found *intercept) {
	s.currentInterceptsLock.Lock()
	for _, ic := range s.currentIntercepts {
		if ic.Spec.Name == name {
			found = ic
			break
		}
	}
	s.currentInterceptsLock.Unlock()
	return found
}

// InterceptsForWorkload returns the client's current intercepts on the given namespace and workload combination.
func (s *session) InterceptsForWorkload(workloadName, namespace string) []*manager.InterceptSpec {
	wlis := make([]*manager.InterceptSpec, 0)
	for _, ic := range s.getCurrentIntercepts() {
		if ic.Spec.Agent == workloadName && ic.Spec.Namespace == namespace {
			wlis = append(wlis, ic.Spec)
		}
	}
	return wlis
}

// ClearIngestsAndIntercepts removes all intercepts.
func (s *session) ClearIngestsAndIntercepts(c context.Context) error {
	for _, ic := range s.getCurrentIntercepts() {
		dlog.Debugf(c, "Clearing intercept %s", ic.Spec.Name)
		err := s.removeIntercept(c, ic)
		if err != nil && grpcStatus.Code(err) != grpcCodes.NotFound {
			return err
		}
	}
	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		dlog.Debugf(c, "Clearing ingest %s", key)
		s.stopHandler(c, key.workload+"/"+key.container, ig.handlerContainer, ig.pid)
		return true
	})
	return nil
}

// reconcileAPIServers start/stop API servers as needed based on the TELEPRESENCE_API_PORT environment variable
// of the currently intercepted agent's env.
func (s *session) reconcileAPIServers(ctx context.Context) {
	wantedPorts := make(map[int]struct{})
	wantedMatchers := make(map[string]*manager.InterceptInfo)

	agentAPIPort := func(ii *manager.InterceptInfo) int {
		is := ii.Spec
		if ps, ok := ii.Environment[agentconfig.EnvAPIPort]; ok {
			port, err := strconv.ParseUint(ps, 10, 16)
			if err == nil {
				return int(port)
			}
			dlog.Errorf(ctx, "unable to parse TELEPRESENCE_API_PORT(%q) to a port number in agent %s.%s: %v", ps, is.Agent, is.Namespace, err)
		}
		return 0
	}

	for _, ic := range s.currentIntercepts {
		ii := ic.InterceptInfo
		if ic.Disposition == manager.InterceptDispositionType_ACTIVE {
			if port := agentAPIPort(ii); port > 0 {
				wantedPorts[port] = struct{}{}
				wantedMatchers[ic.Id] = ii
			}
		}
	}
	for p, as := range s.currentAPIServers {
		if _, ok := wantedPorts[p]; !ok {
			as.cancel()
			delete(s.currentAPIServers, p)
		}
	}
	for p := range wantedPorts {
		if _, ok := s.currentAPIServers[p]; !ok {
			s.newAPIServerForPort(ctx, p)
		}
	}
	for id := range s.currentMatchers {
		if _, ok := wantedMatchers[id]; !ok {
			delete(s.currentMatchers, id)
		}
	}
	for id, ic := range wantedMatchers {
		if _, ok := s.currentMatchers[id]; !ok {
			s.newMatcher(ctx, ic)
		}
	}
}

func (s *session) newAPIServerForPort(ctx context.Context, port int) {
	svr := restapi.NewServer(s)
	as := apiServer{Server: svr}
	ctx, as.cancel = context.WithCancel(ctx)
	if s.currentAPIServers == nil {
		s.currentAPIServers = map[int]*apiServer{port: &as}
	} else {
		s.currentAPIServers[port] = &as
	}
	go func() {
		if err := svr.ListenAndServe(ctx, port); err != nil {
			dlog.Error(ctx, err)
		}
	}()
}

func (s *session) newMatcher(ctx context.Context, ic *manager.InterceptInfo) {
	m, err := matcher.NewRequestFromMap(ic.Headers)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	if s.currentMatchers == nil {
		s.currentMatchers = make(map[string]*apiMatcher)
	}
	s.currentMatchers[ic.Id] = &apiMatcher{
		requestMatcher: m,
		metadata:       ic.Metadata,
	}
}

func (s *session) InterceptInfo(ctx context.Context, callerID, path string, _ uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()

	r := &restapi.InterceptInfo{ClientSide: true}
	am := s.currentMatchers[callerID]
	switch {
	case am == nil:
		dlog.Debugf(ctx, "no matcher found for callerID %s", callerID)
	case am.requestMatcher.Matches(path, headers):
		dlog.Debugf(ctx, "%s: matcher %s\nmatches path %q and headers\n%s", callerID, am.requestMatcher, path, matcher.HeaderStringer(headers))
		r.Intercepted = true
		r.Metadata = am.metadata
	default:
		dlog.Debugf(ctx, "%s: matcher %s\nmatches path %q and headers\n%s", callerID, am.requestMatcher, path, matcher.HeaderStringer(headers))
	}
	return r, nil
}
