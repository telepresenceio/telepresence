package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/datawire/dlib/dgroup"
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
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
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

	// containerName is the name or ID of the container that the intercept handler is
	// running in, when it runs in Docker. As with pid, this entry will only be present when
	// the telepresence intercept command spawns a new command using --docker-run or
	// --docker-build
	containerName string

	// The mounter of the remote file system.
	remotefs.Mounter

	// Use bridged ftp/sftp mount through this local port
	localMountPort int32
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

	waitCh chan<- interceptResult
}

// podInterceptKey identifies an intercepted pod. Although an intercept may span multiple
// pods, the user daemon will always choose exactly one pod with an active intercept to
// do port forwards and remote mounts.
type podInterceptKey struct {
	Id    string
	PodIP string
}

// The podIntercept provides pod specific synchronization for cancellation of port forwards
// and mounts. Cancellation here does not mean that the intercept is cancelled. It just
// means that the given pod is no longer the chosen one. This typically happens when pods
// are scaled down and then up again.
type podIntercept struct {
	wg        sync.WaitGroup
	cancelPod context.CancelFunc
}

// podIntercepts is what the traffic-manager is using to keep track of the chosen pods for
// the currently active intercepts.
type podIntercepts struct {
	sync.Mutex

	// alive contains a map of the currently alive pod intercepts
	alivePods map[podInterceptKey]*podIntercept

	// snapshot is recreated for each new intercept snapshot read from the manager.
	// The set controls which podIntercepts that are considered alive when cancelUnwanted
	// is called
	snapshot map[podInterceptKey]struct{}

	// mountsDone contains channels that are closed when the mounts are prepared for the
	// given id and podIP
	mountsDone map[podInterceptKey]chan struct{}
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

func (ic *intercept) shouldForward() bool {
	return len(ic.localPorts()) > 0
}

// startForwards starts port forwards and mounts for the given podInterceptKey.
// It assumes that the user has called shouldForward and is sure that something will be started.
func (ic *intercept) startForwards(ctx context.Context, wg *sync.WaitGroup) {
	for _, port := range ic.localPorts() {
		var pfCtx context.Context
		if iputil.IsIpV6Addr(ic.PodIp) {
			pfCtx = dgroup.WithGoroutineName(ctx, fmt.Sprintf("/[%s]:%s", ic.PodIp, port))
		} else {
			pfCtx = dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%s", ic.PodIp, port))
		}
		wg.Add(1)
		go ic.workerPortForward(pfCtx, port, wg)
	}
}

func (ic *intercept) workerPortForward(ctx context.Context, port string, wg *sync.WaitGroup) {
	defer wg.Done()
	pp, err := agentconfig.NewPortAndProto(port)
	if err != nil {
		dlog.Errorf(ctx, "malformed extra port %q: %v", port, err)
		return
	}
	addr, err := pp.Addr()
	if err != nil {
		dlog.Errorf(ctx, "unable to resolve extra port %q: %v", port, err)
		return
	}
	f := forwarder.NewInterceptor(addr, ic.PodIp, pp.Port)
	err = f.Serve(ctx, nil)
	if err != nil && ctx.Err() == nil {
		dlog.Errorf(ctx, "port-forwarder failed with %v", err)
	}
}

func newPodIntercepts() *podIntercepts {
	return &podIntercepts{alivePods: make(map[podInterceptKey]*podIntercept)}
}

// start a port forward for the given intercept and remembers that it's alive.
func (lpf *podIntercepts) start(ctx context.Context, ic *intercept, rd daemon.DaemonClient) {
	// The mounts performed here are synced on by podIP + port to keep track of active
	// mounts. This is not enough in situations when a pod is deleted and another pod
	// takes over. That is two different IPs so an additional synchronization on the actual
	// mount point is necessary to prevent that it is established and deleted at the same
	// time.
	fk := podInterceptKey{
		Id:    ic.Id,
		PodIP: ic.PodIp,
	}

	defer func() {
		if md, ok := lpf.mountsDone[fk]; ok {
			delete(lpf.mountsDone, fk)
			close(md)
		}
	}()

	if !ic.shouldForward() && !ic.shouldMount() {
		dlog.Debugf(ctx, "No mounts or port-forwards needed for %+v", fk)
		return
	}

	// Make part of current snapshot tracking so that it isn't removed once the
	// snapshot has been completely handled
	lpf.snapshot[fk] = struct{}{}

	// Already started?
	if _, isLive := lpf.alivePods[fk]; isLive {
		dlog.Debugf(ctx, "Mounts and port-forwards already active for %+v", fk)
		return
	}

	if client.GetConfig(ctx).Cluster().AgentPortForward {
		// An agent port-forward to the pod with a designated to the PodIP is necessary in order to
		// mount or port-forward to localhost.
		_, err := rd.WaitForAgentIP(ctx, &daemon.WaitForAgentIPRequest{
			Ip:      iputil.Parse(ic.PodIp),
			Timeout: durationpb.New(10 * time.Second),
		})
		switch grpcStatus.Code(err) {
		// Unavailable means that the feature disabled. This is OK, the traffic-manager will do the forwarding
		case grpcCodes.OK, grpcCodes.Unavailable:
		case grpcCodes.DeadlineExceeded:
			dlog.Errorf(ctx, "timeout waiting for port-forward to traffic-agent with pod-ip %s", ic.PodIp)
			return
		default:
			dlog.Errorf(ctx, "unexpected error for port-forward to traffic-agent with pod-ip %s: %v", ic.PodIp, err)
			return
		}
	}

	ctx, cancel := context.WithCancel(ic.ctx)
	lp := &podIntercept{cancelPod: cancel}
	if ic.shouldMount() {
		ic.startMount(ctx, &ic.wg, &lp.wg)
	}
	if ic.shouldForward() {
		ic.startForwards(ctx, &lp.wg)
	}
	dlog.Debugf(ctx, "Started mounts and port-forwards for %+v", fk)
	lpf.alivePods[fk] = lp
}

// initSnapshot prepares this instance for a new round of start calls followed by a cancelUnwanted.
func (lpf *podIntercepts) initSnapshot() {
	lpf.snapshot = make(map[podInterceptKey]struct{})
	lpf.mountsDone = make(map[podInterceptKey]chan struct{})
}

func (lpf *podIntercepts) getOrCreateMountsDone(ic *intercept) <-chan struct{} {
	fk := podInterceptKey{Id: ic.Id, PodIP: ic.PodIp}
	md, ok := lpf.mountsDone[fk]
	if !ok {
		md = make(chan struct{})
		lpf.mountsDone[fk] = md
	}
	return md
}

// cancelUnwanted cancels all port forwards that hasn't been started since initSnapshot.
func (lpf *podIntercepts) cancelUnwanted(ctx context.Context) {
	for fk, lp := range lpf.alivePods {
		if _, isWanted := lpf.snapshot[fk]; !isWanted {
			dlog.Infof(ctx, "Terminating mounts and port-forwards for %+v", fk)
			lp.cancelPod()
			delete(lpf.alivePods, fk)
			md, ok := lpf.mountsDone[fk]
			if ok {
				delete(lpf.mountsDone, fk)
				close(md)
			}
			lp.wg.Wait()
		}
	}
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
	podIcepts := newPodIntercepts()
	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are cancelled correctly.
			s.handleInterceptSnapshot(ctx, podIcepts, nil)
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				// Normal termination
				return nil
			}
			return fmt.Errorf("manager.WatchIntercepts recv: %w", err)
		}
		s.handleInterceptSnapshot(ctx, podIcepts, snapshot.Intercepts)
	}
	return nil
}

func (s *session) handleInterceptSnapshot(ctx context.Context, podIcepts *podIntercepts, intercepts []*manager.InterceptInfo) {
	s.setCurrentIntercepts(ctx, intercepts)
	podIcepts.initSnapshot()

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
		if aw != nil {
			dlog.Debugf(ctx, "wait status: intercept id=%q is no longer WAITING; is now %v", ii.Id, ii.Disposition)
			ir := interceptResult{
				intercept:  ic,
				err:        err,
				mountsDone: podIcepts.getOrCreateMountsDone(ic),
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
			ic.FtpPort = 0
			ic.SftpPort = 0
		}
		podIcepts.start(ctx, ic, s.rootDaemon)
	}
	podIcepts.cancelUnwanted(ctx)
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
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	spec := ir.Spec
	for _, iCept := range s.currentIntercepts {
		switch {
		case iCept.Spec.Name == spec.Name:
			return InterceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.New(spec.Name))
		case iCept.Spec.TargetPort == spec.TargetPort && iCept.Spec.TargetHost == spec.TargetHost:
			return &rpc.InterceptResult{
				Error:         common.InterceptError_LOCAL_TARGET_IN_USE,
				ErrorText:     spec.Name,
				ErrorCategory: int32(errcat.User),
				InterceptInfo: iCept.InterceptInfo,
			}
		case ir.MountPoint != "" && iCept.ClientMountPoint == ir.MountPoint:
			return &rpc.InterceptResult{
				Error:         common.InterceptError_MOUNT_POINT_BUSY,
				ErrorText:     spec.Name,
				ErrorCategory: int32(errcat.User),
				InterceptInfo: iCept.InterceptInfo,
			}
		}
	}
	return nil
}

// CanIntercept checks if it is possible to create an intercept for the given request. The intercept can proceed
// only if the returned rpc.InterceptResult is nil. The returned runtime.Object is either nil, indicating a local
// intercept, or the workload for the intercept.
func (s *session) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (userd.InterceptInfo, *rpc.InterceptResult) {
	s.waitForSync(c)
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

	mgrIr := &manager.CreateInterceptRequest{
		Session:       s.SessionInfo(),
		InterceptSpec: spec,
	}
	if er := self.InterceptProlog(c, mgrIr); er != nil {
		return nil, er
	}
	pi, err := s.managerClient.PrepareIntercept(c, mgrIr)
	if err != nil {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	if pi.Error != "" {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.Category(pi.ErrorCategory).Newf(pi.Error))
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

	spec.Client = s.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	mgrClient := self.ManagerClient()

	// iInfo.preparedIntercept == nil means that we're using an older traffic-manager, incapable
	// of using PrepareIntercept.
	pi := iInfo.PreparedIntercept()
	// Make spec port identifier unambiguous.
	spec.ServiceName = pi.ServiceName
	spec.ServicePortName = pi.ServicePortName
	spec.ServicePort = pi.ServicePort
	spec.Protocol = pi.Protocol
	pti, err := iInfo.PortIdentifier()
	if err != nil {
		return InterceptError(common.InterceptError_MISCONFIGURED_WORKLOAD, err)
	}
	spec.ServicePortIdentifier = pti.String()
	result = iInfo.InterceptResult()

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

	// No use trying to kill processes when using a container based daemon, unless
	// that container based daemon runs as a normal user daemon with separate root daemon.
	// Some users run a standard telepresence client together with intercepts in one
	// single container.
	if !(proc.RunningInContainer() && userd.GetService(c).RootSessionInProcess()) {
		if ic.containerName != "" {
			if err := docker.StopContainer(docker.EnableClient(c), ic.containerName); err != nil {
				dlog.Error(c, err)
			}
		} else if ic.pid != 0 {
			p, err := os.FindProcess(ic.pid)
			if err != nil {
				dlog.Errorf(c, "unable to find interceptor for intercept %s with pid %d", name, ic.pid)
			} else {
				dlog.Debugf(c, "terminating interceptor for intercept %s with pid %d", name, ic.pid)
				_ = proc.Terminate(p)
			}
		}
	}

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

// AddInterceptor associates the given intercept with a running process. This ensures that
// the running process will be signalled when the intercept is removed.
func (s *session) AddInterceptor(id string, ih *rpc.Interceptor) error {
	s.currentInterceptsLock.Lock()
	if ci, ok := s.currentIntercepts[id]; ok {
		ci.pid = int(ih.Pid)
		ci.containerName = ih.ContainerName
	}
	s.currentInterceptsLock.Unlock()
	return nil
}

func (s *session) RemoveInterceptor(id string) error {
	s.currentInterceptsLock.Lock()
	if ci, ok := s.currentIntercepts[id]; ok {
		ci.pid = 0
		ci.containerName = ""
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
		if ic.containerName != "" {
			if ii.Environment == nil {
				ii.Environment = make(map[string]string, 1)
			}
			ii.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"] = ic.containerName
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

// ClearIntercepts removes all intercepts.
func (s *session) ClearIntercepts(c context.Context) error {
	for _, ic := range s.getCurrentIntercepts() {
		dlog.Debugf(c, "Clearing intercept %s", ic.Spec.Name)
		err := s.removeIntercept(c, ic)
		if err != nil && grpcStatus.Code(err) != grpcCodes.NotFound {
			return err
		}
	}
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
