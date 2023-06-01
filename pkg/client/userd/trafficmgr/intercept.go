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

	"github.com/blang/semver"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
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
	intercept *intercept
	err       error
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
func (lpf *podIntercepts) start(ic *intercept) {
	if !ic.shouldForward() && !ic.shouldMount() {
		return
	}

	// The mounts performed here are synced on by podIP + port to keep track of active
	// mounts. This is not enough in situations when a pod is deleted and another pod
	// takes over. That is two different IPs so an additional synchronization on the actual
	// mount point is necessary to prevent that it is established and deleted at the same
	// time.
	fk := podInterceptKey{
		Id:    ic.Id,
		PodIP: ic.PodIp,
	}

	// Make part of current snapshot tracking so that it isn't removed once the
	// snapshot has been completely handled
	lpf.snapshot[fk] = struct{}{}

	// Already started?
	if _, isLive := lpf.alivePods[fk]; isLive {
		return
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
}

// cancelUnwanted cancels all port forwards that hasn't been started since initSnapshot.
func (lpf *podIntercepts) cancelUnwanted(ctx context.Context) {
	for fk, lp := range lpf.alivePods {
		if _, isWanted := lpf.snapshot[fk]; !isWanted {
			dlog.Infof(ctx, "Terminating mounts and port-forwards for %+v", fk)
			lp.cancelPod()
			delete(lpf.alivePods, fk)
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
	s.currentInterceptsLock.Lock()
	active := len(s.localIntercepts)
	ins := s.interceptedNamespace
	s.currentInterceptsLock.Unlock()

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
			active++
			ns := ii.Spec.Namespace
			if ins == "" {
				ins = ns
			} else if ins != ns {
				err = errcat.User.Newf("active intercepts in both namespace %s and %s", ns, ins)
			}
		} else {
			err = fmt.Errorf("intercept in error state %v: %v", ii.Disposition, ii.Message)
		}

		// Notify waiters for active intercepts
		if aw != nil {
			dlog.Debugf(ctx, "wait status: intercept id=%q is no longer WAITING; is now %v", ii.Id, ii.Disposition)
			select {
			case aw.waitCh <- interceptResult{
				intercept: ic,
				err:       err,
			}:
			default:
				// Channel was closed
			}
		}
		if err != nil {
			continue
		}

		if s.isPodDaemon {
			// disable mount point logic
			ic.FtpPort = 0
			ic.SftpPort = 0
		}
		podIcepts.start(ic)
	}
	if active == 0 {
		ins = ""
	}
	s.setInterceptedNamespace(ctx, ins)
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

	// apiKey if the user is logged in
	apiKey string

	// Fields below are all deprecated and only used with traffic-manager < 2.6.0
	// Deprecated
	service *core.Service
	// Deprecated
	servicePort *core.ServicePort
	// Deprecated
	workload k8sapi.Workload
	// Deprecated
	container *core.Container
	// Deprecated
	containerPortIndex int
}

func (s *interceptInfo) APIKey() string {
	return s.apiKey
}

func (s *interceptInfo) InterceptResult() *rpc.InterceptResult {
	if pi := s.preparedIntercept; pi != nil {
		return &rpc.InterceptResult{
			ServiceUid:   pi.ServiceUid,
			WorkloadKind: pi.WorkloadKind,
		}
	}
	return &rpc.InterceptResult{
		ServiceUid:   string(s.service.UID),
		WorkloadKind: s.workload.GetKind(),
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
	if s.interceptedNamespace != "" && s.interceptedNamespace != spec.Namespace {
		return InterceptError(common.InterceptError_NAMESPACE_AMBIGUITY, errcat.User.Newf("%s,%s", s.interceptedNamespace, spec.Namespace))
	}
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
func CanIntercept(sif userd.Session, c context.Context, ir *rpc.CreateInterceptRequest) (userd.InterceptInfo, *rpc.InterceptResult) {
	var s *session
	sif.As(&s)

	s.waitForSync(c)
	spec := ir.Spec
	spec.Namespace = s.ActualNamespace(spec.Namespace)
	if spec.Namespace == "" {
		// namespace is not currently mapped
		return nil, InterceptError(common.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(ir.Spec.Agent))
	}

	if _, inUse := s.localIntercepts[spec.Name]; inUse {
		return nil, InterceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
	}

	if er := s.ensureNoInterceptConflict(ir); er != nil {
		return nil, er
	}
	if spec.Agent == "" {
		return nil, nil
	}

	if s.managerVersion.LT(firstAgentConfigMapVersion) {
		// fall back traffic-manager behaviour prior to 2.6
		return s.legacyCanInterceptEpilog(c, ir)
	}

	mgrIr := &manager.CreateInterceptRequest{
		Session:       s.SessionInfo(),
		InterceptSpec: spec,
	}
	if er := sif.InterceptProlog(c, mgrIr); er != nil {
		return nil, er
	}
	pi, err := s.managerClient.PrepareIntercept(c, mgrIr)
	if err != nil {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	if pi.Error != "" {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.Category(pi.ErrorCategory).Newf(pi.Error))
	}

	iInfo := &interceptInfo{preparedIntercept: pi, apiKey: mgrIr.ApiKey}
	return iInfo, nil
}

// legacyImage ensures that the installer never modifies a workload to
// install a version that is more recent than the traffic-manager currently
// in use (it's legacy too, or we wouldn't end up here)
// Deprecated.
func (s *session) legacyImage(ctx context.Context, image string) (string, error) {
	if image == "" {
		var err error
		image, err = AgentImageFromSystemA(ctx, s.managerVersion)
		if err != nil {
			return "", err
		}
	}
	if lc := strings.LastIndexByte(image, ':'); lc > 0 {
		lc++
		img := image[:lc]
		if iv, err := semver.Parse(image[lc:]); err == nil {
			if strings.HasSuffix(img, "/tel2:") {
				if iv.Major > 2 || iv.Minor > 5 {
					image = img + s.managerVersion.String()
				}
			} else if strings.HasSuffix(img, "/ambassador-telepresence-agent:") {
				if iv.Major > 1 || iv.Minor > 11 {
					image = img + "1.11.11"
				}
			}
		}
	}
	return image, nil
}

// Deprecated.
func (s *session) legacyCanInterceptEpilog(c context.Context, ir *rpc.CreateInterceptRequest) (*interceptInfo, *rpc.InterceptResult) {
	var err error
	if ir.AgentImage, err = s.legacyImage(c, ir.AgentImage); err != nil {
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	spec := ir.Spec
	wl, err := tracing.GetWorkload(c, spec.Agent, spec.Namespace, spec.WorkloadKind)
	if err != nil {
		if errors2.IsNotFound(err) {
			return nil, InterceptError(common.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(spec.Name))
		}
		err = fmt.Errorf("failed to get workload %s.%s: %w", spec.Agent, spec.Namespace, err)
		dlog.Error(c, err)
		return nil, InterceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}

	iInfo, err := exploreSvc(c, spec.ServicePortIdentifier, spec.ServiceName, wl)
	if err != nil {
		return nil, InterceptError(common.InterceptError_FAILED_TO_ESTABLISH, err)
	}
	return iInfo, nil
}

// AddIntercept adds one intercept.
func AddIntercept(sif userd.Session, c context.Context, ir *rpc.CreateInterceptRequest) *rpc.InterceptResult {
	iInfo, result := CanIntercept(sif, c, ir)
	if result != nil {
		return result
	}

	var s *session
	sif.As(&s)

	spec := ir.Spec
	if iInfo == nil {
		return s.addLocalOnlyIntercept(c, spec)
	}

	spec.Client = s.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	cfg := client.GetConfig(c)
	apiPort := uint16(cfg.TelepresenceAPI.Port)
	if apiPort == 0 {
		// Default to the API port declared by the traffic-manager
		if apiInfo, err := s.managerClient.GetTelepresenceAPI(c, &empty.Empty{}); err != nil {
			// Traffic manager is probably outdated. Not fatal, but deserves to be logged
			dlog.Warnf(c, "failed to obtain Telepresence API info from traffic manager: %v", err)
		} else {
			apiPort = uint16(apiInfo.Port)
		}
	}

	var agentEnv map[string]string
	// iInfo.preparedIntercept == nil means that we're using an older traffic-manager, incapable
	// of using PrepareIntercept.
	if pi := iInfo.PreparedIntercept(); pi == nil {
		// It's OK to just call addAgent every time; if the agent is already installed then it's a
		// no-op.
		agentEnv, result = s.addAgent(c, iInfo.(*interceptInfo), ir.AgentImage, apiPort)
		if result.Error != common.InterceptError_UNSPECIFIED {
			return result
		}
	} else {
		// Make spec port identifier unambiguous.
		spec.ServiceName = pi.ServiceName
		spec.ServicePortName = pi.ServicePortName
		spec.ServicePort = pi.ServicePort
		spec.Protocol = pi.Protocol
		pi, err := iInfo.PortIdentifier()
		if err != nil {
			return InterceptError(common.InterceptError_MISCONFIGURED_WORKLOAD, err)
		}
		spec.ServicePortIdentifier = pi.String()
		result = iInfo.InterceptResult()
	}

	spec.ServiceUid = result.ServiceUid
	spec.WorkloadKind = result.WorkloadKind

	dlog.Debugf(c, "creating intercept %s", spec.Name)
	tos := &client.GetConfig(c).Timeouts
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

	ii, err := sif.ManagerClient().CreateIntercept(c, &manager.CreateInterceptRequest{
		Session:       sif.SessionInfo(),
		InterceptSpec: spec,
		ApiKey:        iInfo.APIKey(),
	})
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
			rc, cancel := context.WithTimeout(dcontext.WithoutCancel(c), 5*time.Second)
			defer cancel()
			if removeErr := sif.RemoveIntercept(rc, ii.Spec.Name); removeErr != nil {
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
			// Older traffic-managers pass env in the agent info
			if agentEnv != nil {
				ii.Environment = agentEnv
			}
			result.InterceptInfo = ii
			if !waitForDNS(c, spec.ServiceName) {
				dlog.Warningf(c, "DNS cannot resolve name of intercepted %q service", spec.ServiceName)
			}
			if er := sif.InterceptEpilog(c, ir, result); er != nil {
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

func waitForDNS(c context.Context, host string) bool {
	c, cancel := context.WithTimeout(c, 12*time.Second)
	defer cancel()
	for c.Err() == nil {
		dtime.SleepWithContext(c, 200*time.Millisecond)
		dlog.Debugf(c, "Attempting to resolve DNS for %s", host)
		ips := dnsproxy.TimedExternalLookup(c, host, 5*time.Second)
		if len(ips) > 0 {
			dlog.Debugf(c, "Attempt succeeded, DNS for %s is %v", host, ips)
			return true
		}
	}
	return false
}

// RemoveIntercept removes one intercept by name.
func (s *session) RemoveIntercept(c context.Context, name string) error {
	dlog.Debugf(c, "Removing intercept %s", name)

	if _, ok := s.localIntercepts[name]; ok {
		s.RemoveLocalOnlyIntercept(c, name)
		return nil
	}

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
			c, err := docker.EnableClient(c)
			if err == nil {
				err = docker.StopContainer(c, ic.containerName)
			}
			if err != nil {
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
	c, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
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
	if _, ok := s.localIntercepts[name]; ok {
		s.currentInterceptsLock.Lock()
		ns := s.interceptedNamespace
		s.currentInterceptsLock.Unlock()
		return &manager.InterceptSpec{Name: name, Namespace: ns, WorkloadKind: "local"}
	}
	if ic := s.getInterceptByName(name); ic != nil {
		return ic.Spec
	}
	return nil
}

// GetInterceptInfo returns the InterceptInfo for the given name, or nil if no such info exists.
func (s *session) GetInterceptInfo(name string) *manager.InterceptInfo {
	if _, ok := s.localIntercepts[name]; ok {
		return &manager.InterceptInfo{Spec: s.GetInterceptSpec(name)}
	}
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
	for ic := range s.localIntercepts {
		dlog.Debugf(c, "Clearing local-only intercept %s", ic)
		s.RemoveLocalOnlyIntercept(c, ic)
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

func (s *session) addLocalOnlyIntercept(c context.Context, spec *manager.InterceptSpec) *rpc.InterceptResult {
	s.currentInterceptsLock.Lock()
	update := false
	s.localIntercepts[spec.Name] = struct{}{}
	if s.interceptedNamespace == "" {
		s.interceptedNamespace = spec.Namespace
		update = true
	}
	s.currentInterceptsLock.Unlock()
	if update {
		s.updateDaemonNamespaces(c)
	}
	return &rpc.InterceptResult{
		InterceptInfo: &manager.InterceptInfo{
			Spec:              spec,
			Disposition:       manager.InterceptDispositionType_ACTIVE,
			MechanismArgsDesc: "as local-only",
			ClientSession:     s.sessionInfo,
		},
	}
}

func (s *session) RemoveLocalOnlyIntercept(c context.Context, name string) {
	dlog.Debugf(c, "removing local-only intercept %s", name)

	// Ensure that namespace is removed from localInterceptedNamespaces if this was the last local intercept
	// for the given namespace.
	s.currentInterceptsLock.Lock()
	delete(s.localIntercepts, name)
	if len(s.localIntercepts) == 0 && len(s.currentIntercepts) == 0 {
		s.interceptedNamespace = ""
	}
	s.currentInterceptsLock.Unlock()
	s.updateDaemonNamespaces(c)
}
