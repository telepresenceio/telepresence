package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
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
	rpc2 "github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type mounter interface {
	start(ctx context.Context, ic *intercept) error
}

// intercept tracks the life-cycle of an intercept, dictated by the intercepts
// arrival and departure in the watchInterceptsLoop
type intercept struct {
	sync.Mutex
	*manager.InterceptInfo

	// ctx is a context cancelled by the cancel attribute. It must be used by
	// services that should be cancelled when the intercept ends
	ctx context.Context

	// cancel is called when the intercept is no longer present
	cancel context.CancelFunc

	// pid of interceptor owned by an intercept. This entry will only be present when
	// the telepresence intercept command spawns a new command. The int value reflects
	// the pid of that new command.
	pid int

	// The mounter of the remote file system.
	mounter
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
	waitCh     chan<- interceptResult
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
		pfCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%s", ic.PodIp, port))
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

// start a port forward for the given intercept and remembers that it's alive
func (lpf *podIntercepts) start(ctx context.Context, ic *intercept, fuseftp rpc2.FuseFTPClient) {
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

	ctx, cancel := context.WithCancel(ctx)
	lp := &podIntercept{cancelPod: cancel}
	if ic.shouldMount() {
		ic.startMount(ctx, fuseftp, &lp.wg)
	}
	if ic.shouldForward() {
		ic.startForwards(ctx, &lp.wg)
	}
	dlog.Debugf(ctx, "Started mounts and port-forwards for %+v", fk)
	lpf.alivePods[fk] = lp
}

// initSnapshot prepares this instance for a new round of start calls followed by a cancelUnwanted
func (lpf *podIntercepts) initSnapshot() {
	lpf.snapshot = make(map[podInterceptKey]struct{})
}

// cancelUnwanted cancels all port forwards that hasn't been started since initSnapshot
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

func (tm *TrafficManager) watchInterceptsHandler(ctx context.Context) error {
	// Don't use a dgroup.Group because:
	//  1. we don't actually care about tracking errors (we just always retry) or any of
	//     dgroup's other functionality
	//  2. because goroutines may churn as intercepts are created and deleted, tracking all of
	//     their exit statuses is just a memory leak
	//  3. because we want a per-worker cancel, we'd have to implement our own Context
	//     management on top anyway, so dgroup wouldn't actually save us any complexity.
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := tm.watchInterceptsLoop(ctx); err != nil {
			dlog.Error(ctx, err)
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
		}
	}
	return nil
}

func (tm *TrafficManager) watchInterceptsLoop(ctx context.Context) error {
	stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
	if err != nil {
		return fmt.Errorf("manager.WatchIntercepts dial: %w", err)
	}
	podIcepts := newPodIntercepts()
	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are cancelled correctly.
			tm.handleInterceptSnapshot(ctx, podIcepts, nil)
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				// Normal termination
				return nil
			}
			return fmt.Errorf("manager.WatchIntercepts recv: %w", err)
		}
		tm.handleInterceptSnapshot(ctx, podIcepts, snapshot.Intercepts)
	}
	return nil
}

func (tm *TrafficManager) handleInterceptSnapshot(ctx context.Context, podIcepts *podIntercepts, intercepts []*manager.InterceptInfo) {
	tm.setCurrentIntercepts(ctx, intercepts)
	podIcepts.initSnapshot()
	namespaces := make(map[string]struct{})
	for _, ii := range intercepts {
		if ii.Disposition == manager.InterceptDispositionType_WAITING {
			continue
		}

		tm.currentInterceptsLock.Lock()
		ic := tm.currentIntercepts[ii.Id]
		aw := tm.interceptWaiters[ii.Spec.Name]
		if aw != nil {
			delete(tm.interceptWaiters, ii.Spec.Name)
		}
		tm.currentInterceptsLock.Unlock()

		var err error
		if ii.Disposition != manager.InterceptDispositionType_ACTIVE {
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

		namespaces[ii.Spec.Namespace] = struct{}{}
		if tm.isPodDaemon {
			// disable mount point logic
			ic.FtpPort = 0
			ic.SftpPort = 0
		}
		podIcepts.start(ctx, ic, tm.fuseFtp)
	}
	podIcepts.cancelUnwanted(ctx)
	if ctx.Err() == nil && !tm.isPodDaemon {
		tm.setInterceptedNamespaces(ctx, namespaces)
	}
}

// getCurrentIntercepts returns a copy of the current intercept snapshot
func (tm *TrafficManager) getCurrentIntercepts() []*intercept {
	// Copy the current snapshot
	tm.currentInterceptsLock.Lock()
	sz := len(tm.currentIntercepts)
	intercepts := make([]*intercept, sz)
	ids := make([]string, sz)
	idx := 0
	for id := range tm.currentIntercepts {
		ids[idx] = id
		idx++
	}
	sort.Strings(ids)
	for idx, id := range ids {
		intercepts[idx] = tm.currentIntercepts[id]
	}
	tm.currentInterceptsLock.Unlock()
	return intercepts
}

// getCurrentInterceptInfos returns the InterceptInfos of the current intercept snapshot
func (tm *TrafficManager) getCurrentInterceptInfos() []*manager.InterceptInfo {
	// Copy the current snapshot
	ics := tm.getCurrentIntercepts()
	ifs := make([]*manager.InterceptInfo, len(ics))
	for idx, ic := range ics {
		ifs[idx] = ic.InterceptInfo
	}
	return ifs
}

func (tm *TrafficManager) setCurrentIntercepts(ctx context.Context, iis []*manager.InterceptInfo) {
	tm.currentInterceptsLock.Lock()
	defer tm.currentInterceptsLock.Unlock()
	intercepts := make(map[string]*intercept, len(iis))
	sb := strings.Builder{}
	sb.WriteByte('[')
	for i, ii := range iis {
		ic, ok := tm.currentIntercepts[ii.Id]
		if ok {
			// retain ClientMountPoint, it's assigned in the client and never passed from the traffic-manager
			ii.ClientMountPoint = ic.ClientMountPoint
			ic.InterceptInfo = ii
		} else {
			ic = &intercept{InterceptInfo: ii}
			ic.ctx, ic.cancel = context.WithCancel(ctx)
			dlog.Debugf(ctx, "Received new intercept %s", ic.Spec.Name)
			if aw, ok := tm.interceptWaiters[ii.Spec.Name]; ok {
				ic.ClientMountPoint = aw.mountPoint
			}
		}
		intercepts[ii.Id] = ic
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(ii.Spec.Name)
	}
	sb.WriteByte(']')
	dlog.Debugf(ctx, "setCurrentIntercepts(%s)", sb.String())

	// Cancel those that no longer exists
	for id, ic := range tm.currentIntercepts {
		if _, ok := intercepts[id]; !ok {
			dlog.Debugf(ctx, "Cancelling context for intercept %s", ic.Spec.Name)
			ic.cancel()
		}
	}
	tm.currentIntercepts = intercepts
	tm.reconcileAPIServers(ctx)
}

func interceptError(tp common.InterceptError, err error) *rpc.InterceptResult {
	return &rpc.InterceptResult{
		Error:         tp,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

type serviceProps struct {
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

func (s *serviceProps) interceptResult() *rpc.InterceptResult {
	if pi := s.preparedIntercept; pi != nil {
		return &rpc.InterceptResult{
			ServiceUid:   pi.ServiceUid,
			WorkloadKind: pi.WorkloadKind,
			ServiceProps: &userdaemon.IngressInfoRequest{
				ServiceUid:            pi.ServiceUid,
				ServiceName:           pi.ServiceName,
				ServicePortIdentifier: pi.ServicePortName,
				ServicePort:           pi.ServicePort,
				Namespace:             pi.Namespace,
			},
		}
	}
	return &rpc.InterceptResult{
		ServiceUid:   string(s.service.UID),
		WorkloadKind: s.workload.GetKind(),
		ServiceProps: &userdaemon.IngressInfoRequest{
			ServiceUid:            string(s.service.UID),
			ServiceName:           s.service.Name,
			ServicePortIdentifier: s.servicePort.Name,
			ServicePort:           s.servicePort.Port,
			Namespace:             s.service.Namespace,
		},
	}
}

func (s *serviceProps) portIdentifier() (agentconfig.PortIdentifier, error) {
	var spi string
	if s.preparedIntercept.ServicePortName == "" {
		spi = strconv.Itoa(int(s.preparedIntercept.ServicePort))
	} else {
		spi = s.preparedIntercept.ServicePortName
	}
	return agentconfig.NewPortIdentifier(s.preparedIntercept.Protocol, spi)
}

func (tm *TrafficManager) ensureNoInterceptConflict(ir *rpc.CreateInterceptRequest) *rpc.InterceptResult {
	tm.currentInterceptsLock.Lock()
	defer tm.currentInterceptsLock.Unlock()
	spec := ir.Spec
	for _, iCept := range tm.currentIntercepts {
		switch {
		case iCept.Spec.Name == spec.Name:
			return interceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
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
func (tm *TrafficManager) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (*serviceProps, *rpc.InterceptResult) {
	tm.waitForSync(c)
	spec := ir.Spec
	spec.Namespace = tm.ActualNamespace(spec.Namespace)
	if spec.Namespace == "" {
		// namespace is not currently mapped
		return nil, interceptError(common.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(ir.Spec.Agent))
	}

	if _, inUse := tm.localIntercepts[spec.Name]; inUse {
		return nil, interceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
	}

	if er := tm.ensureNoInterceptConflict(ir); er != nil {
		return nil, er
	}
	if spec.Agent == "" {
		return nil, nil
	}

	apiKey, err := tm.getCloudAPIKey(c, a8rcloud.KeyDescAgent(spec), false)
	if err != nil {
		if !errors.Is(err, auth.ErrNotLoggedIn) {
			dlog.Errorf(c, "error getting apiKey for agent: %s", err)
		}
	}

	if tm.managerVersion.LT(firstAgentConfigMapVersion) {
		// fall back traffic-manager behaviour prior to 2.6
		return tm.legacyCanInterceptEpilog(c, ir, apiKey)
	}

	pi, err := tm.managerClient.PrepareIntercept(c, &manager.CreateInterceptRequest{
		Session:       tm.session(),
		InterceptSpec: spec,
		ApiKey:        apiKey,
	})
	if err != nil {
		return nil, interceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	if pi.Error != "" {
		return nil, interceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.Category(pi.ErrorCategory).Newf(pi.Error))
	}

	// Verify that the receiving agent can handle the mechanism arguments that are passed to it.
	if newMechanismArgs, err := extensions.MakeArgsCompatible(c, spec.Mechanism, pi.AgentImage, spec.MechanismArgs); err != nil {
		return nil, interceptError(common.InterceptError_UNKNOWN_FLAG, err)
	} else if !reflect.DeepEqual(spec.MechanismArgs, newMechanismArgs) {
		dlog.Infof(c, "Rewriting MechanismArgs from %q to %q", spec.MechanismArgs, newMechanismArgs)
		spec.MechanismArgs = newMechanismArgs
	}

	svcProps := &serviceProps{preparedIntercept: pi, apiKey: apiKey}
	return svcProps, svcProps.interceptResult()
}

// legacyImage ensures that the installer never modifies a workload to
// install a version that is more recent than the traffic-manager currently
// in use (it's legacy too, or we wouldn't end up here)
// Deprecated
func (tm *TrafficManager) legacyImage(image string) string {
	if lc := strings.LastIndexByte(image, ':'); lc > 0 {
		lc++
		img := image[:lc]
		if iv, err := semver.Parse(image[lc:]); err == nil {
			if strings.HasSuffix(img, "/tel2:") {
				if iv.Major > 2 || iv.Minor > 5 {
					image = img + tm.managerVersion.String()
				}
			} else if strings.HasSuffix(img, "/ambassador-telepresence-agent:") {
				if iv.Major > 1 || iv.Minor > 11 {
					image = img + "1.11.11"
				}
			}
		}
	}
	return image
}

// Deprecated
func (tm *TrafficManager) legacyCanInterceptEpilog(c context.Context, ir *rpc.CreateInterceptRequest, apiKey string) (*serviceProps, *rpc.InterceptResult) {
	ir.AgentImage = tm.legacyImage(ir.AgentImage)
	spec := ir.Spec
	wl, err := k8sapi.GetWorkload(c, spec.Agent, spec.Namespace, spec.WorkloadKind)
	if err != nil {
		if errors2.IsNotFound(err) {
			return nil, interceptError(common.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(spec.Name))
		}
		err = fmt.Errorf("failed to get workload %s.%s: %w", spec.Agent, spec.Namespace, err)
		dlog.Error(c, err)
		return nil, interceptError(common.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}

	// Verify that the receiving agent can handle the mechanism arguments that are passed to it.
	podTpl := wl.GetPodTemplate()
	autoInstall, err := useAutoInstall(podTpl)
	if err != nil {
		// useAutoInstall also verifies annotation consistency
		return nil, interceptError(common.InterceptError_MISCONFIGURED_WORKLOAD, errcat.User.New(err))
	}
	var image string
	if autoInstall {
		image = ir.AgentImage
	} else {
		for _, container := range podTpl.Spec.Containers {
			if container.Name == install.AgentContainerName {
				image = container.Image
				break
			}
		}
	}
	if newMechanismArgs, err := extensions.MakeArgsCompatible(c, spec.Mechanism, image, spec.MechanismArgs); err != nil {
		return nil, interceptError(common.InterceptError_UNKNOWN_FLAG, err)
	} else if !reflect.DeepEqual(spec.MechanismArgs, newMechanismArgs) {
		dlog.Infof(c, "Rewriting MechanismArgs from %q to %q", spec.MechanismArgs, newMechanismArgs)
		spec.MechanismArgs = newMechanismArgs
	}

	svcProps, err := exploreSvc(c, spec.ServicePortIdentifier, spec.ServiceName, wl)
	if err != nil {
		return nil, interceptError(common.InterceptError_FAILED_TO_ESTABLISH, err)
	}
	svcProps.apiKey = apiKey
	return svcProps, svcProps.interceptResult()
}

// AddIntercept adds one intercept
func (tm *TrafficManager) AddIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) { //nolint:gocognit // bugger off
	var svcProps *serviceProps
	svcProps, result = tm.CanIntercept(c, ir)
	if result != nil && result.Error != common.InterceptError_UNSPECIFIED {
		return result, nil
	}

	spec := ir.Spec
	if svcProps == nil {
		return tm.AddLocalOnlyIntercept(c, spec)
	}

	spec.Client = tm.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	cfg := client.GetConfig(c)
	apiPort := uint16(cfg.TelepresenceAPI.Port)
	if apiPort == 0 {
		// Default to the API port declared by the traffic-manager
		var apiInfo *manager.TelepresenceAPIInfo
		if apiInfo, err = tm.managerClient.GetTelepresenceAPI(c, &empty.Empty{}); err != nil {
			// Traffic manager is probably outdated. Not fatal, but deserves to be logged
			dlog.Errorf(c, "failed to obtain Telepresence API info from traffic manager: %v", err)
		} else {
			apiPort = uint16(apiInfo.Port)
		}
	}

	var agentEnv map[string]string
	// svcProps.preparedIntercept == nil means that we're using an older traffic-manager, incapable
	// of using PrepareIntercept.
	if svcProps.preparedIntercept == nil {
		// It's OK to just call addAgent every time; if the agent is already installed then it's a
		// no-op.
		agentEnv, result = tm.addAgent(c, svcProps, ir.AgentImage, apiPort)
		if result.Error != common.InterceptError_UNSPECIFIED {
			return result, nil
		}
	} else {
		// Make spec port identifier unambiguous.
		spec.ServiceName = svcProps.preparedIntercept.ServiceName
		pi, err := svcProps.portIdentifier()
		if err != nil {
			return nil, err
		}
		spec.ServicePortIdentifier = pi.String()
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
	tm.currentInterceptsLock.Lock()
	tm.interceptWaiters[spec.Name] = &awaitIntercept{
		mountPoint: ir.MountPoint,
		waitCh:     waitCh,
	}
	tm.currentInterceptsLock.Unlock()
	defer func() {
		tm.currentInterceptsLock.Lock()
		if _, ok := tm.interceptWaiters[spec.Name]; ok {
			delete(tm.interceptWaiters, spec.Name)
			close(waitCh)
		}
		tm.currentInterceptsLock.Unlock()
	}()

	var ii *manager.InterceptInfo
	ii, err = tm.managerClient.CreateIntercept(c, &manager.CreateInterceptRequest{
		Session:       tm.session(),
		InterceptSpec: spec,
		ApiKey:        svcProps.apiKey,
	})
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		err = client.CheckTimeout(c, err)
		code := grpcCodes.Internal
		if errors.Is(err, context.DeadlineExceeded) {
			code = grpcCodes.DeadlineExceeded
		} else if errors.Is(err, context.Canceled) {
			code = grpcCodes.Canceled
		}
		return nil, grpcStatus.Error(code, err.Error())
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
			if removeErr := tm.RemoveIntercept(rc, ii.Spec.Name); removeErr != nil {
				dlog.Warnf(c, "failed to remove failed intercept %s: %v", ii.Spec.Name, removeErr)
			}
		}
	}()

	// Wait for the intercept to transition from WAITING or NO_AGENT to ACTIVE. This
	// might result in more than one event.
	for {
		select {
		case <-c.Done():
			err = client.CheckTimeout(c, c.Err())
			code := grpcCodes.Canceled
			if errors.Is(err, context.DeadlineExceeded) {
				code = grpcCodes.DeadlineExceeded
			}
			err = grpcStatus.Error(code, err.Error())
			return nil, err
		case wr := <-waitCh:
			if wr.err != nil {
				return interceptError(common.InterceptError_FAILED_TO_ESTABLISH, errcat.User.New(wr.err)), nil
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
			success = true
			return result, nil
		}
	}
}

func waitForDNS(c context.Context, host string) bool {
	c, cancel := context.WithTimeout(c, 3*time.Second)
	defer cancel()
	for c.Err() == nil {
		_, err := net.DefaultResolver.LookupIPAddr(c, host)
		if err == nil {
			return true
		}
		dtime.SleepWithContext(c, 5*time.Millisecond)
	}
	return false
}

// RemoveIntercept removes one intercept by name
func (tm *TrafficManager) RemoveIntercept(c context.Context, name string) error {
	dlog.Debugf(c, "Removing intercept %s", name)

	if ns, ok := tm.localIntercepts[name]; ok {
		return tm.RemoveLocalOnlyIntercept(c, name, ns)
	}

	ii := tm.getInterceptByName(name)
	if ii == nil {
		dlog.Debugf(c, "Intercept %s was already removed", name)
		return nil
	}
	return tm.removeIntercept(c, ii)
}

func (tm *TrafficManager) removeIntercept(c context.Context, ic *intercept) error {
	name := ic.Spec.Name
	if ic.pid != 0 {
		p, err := os.FindProcess(ic.pid)
		if err != nil {
			dlog.Errorf(c, "unable to find interceptor for intercept %s with pid %d", name, ic.pid)
		} else {
			dlog.Debugf(c, "terminating interceptor for intercept %s with pid %d", name, ic.pid)
			_ = proc.Terminate(p)
		}
	}

	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	_, err := tm.managerClient.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: tm.session(),
		Name:    name,
	})
	return err
}

// AddInterceptor associates the given interceptId with a pid of a running process. This ensures that
// the running process will be signalled when the intercept is removed
func (tm *TrafficManager) AddInterceptor(s string, i int) error {
	tm.currentInterceptsLock.Lock()
	if ci, ok := tm.currentIntercepts[s]; ok {
		ci.pid = i
	}
	tm.currentInterceptsLock.Unlock()
	return nil
}

func (tm *TrafficManager) RemoveInterceptor(s string) error {
	tm.currentInterceptsLock.Lock()
	if ci, ok := tm.currentIntercepts[s]; ok {
		ci.pid = 0
	}
	tm.currentInterceptsLock.Unlock()
	return nil
}

// GetInterceptSpec returns the InterceptSpec for the given name, or nil if no such spec exists
func (tm *TrafficManager) GetInterceptSpec(name string) *manager.InterceptSpec {
	if ns, ok := tm.localIntercepts[name]; ok {
		return &manager.InterceptSpec{Name: name, Namespace: ns, WorkloadKind: "local"}
	}
	if ic := tm.getInterceptByName(name); ic != nil {
		return ic.Spec
	}
	return nil
}

// GetInterceptSpec returns the InterceptSpec for the given name, or nil if no such spec exists
func (tm *TrafficManager) getInterceptByName(name string) (found *intercept) {
	tm.currentInterceptsLock.Lock()
	for _, ic := range tm.currentIntercepts {
		if ic.Spec.Name == name {
			found = ic
			break
		}
	}
	tm.currentInterceptsLock.Unlock()
	return found
}

// InterceptsForWorkload returns the client's current intercepts on the given namespace and workload combination
func (tm *TrafficManager) InterceptsForWorkload(workloadName, namespace string) []*manager.InterceptSpec {
	wlis := make([]*manager.InterceptSpec, 0)
	for _, ic := range tm.getCurrentIntercepts() {
		if ic.Spec.Agent == workloadName && ic.Spec.Namespace == namespace {
			wlis = append(wlis, ic.Spec)
		}
	}
	return wlis
}

// ClearIntercepts removes all intercepts
func (tm *TrafficManager) ClearIntercepts(c context.Context) error {
	for _, ic := range tm.getCurrentIntercepts() {
		dlog.Debugf(c, "Clearing intercept %s", ic.Spec.Name)
		err := tm.removeIntercept(c, ic)
		if err != nil && grpcStatus.Code(err) != grpcCodes.NotFound {
			return err
		}
	}
	return nil
}

// reconcileAPIServers start/stop API servers as needed based on the TELEPRESENCE_API_PORT environment variable
// of the currently intercepted agent's env.
func (tm *TrafficManager) reconcileAPIServers(ctx context.Context) {
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

	for _, ic := range tm.currentIntercepts {
		ii := ic.InterceptInfo
		if ic.Disposition == manager.InterceptDispositionType_ACTIVE {
			if port := agentAPIPort(ii); port > 0 {
				wantedPorts[port] = struct{}{}
				wantedMatchers[ic.Id] = ii
			}
		}
	}
	for p, as := range tm.currentAPIServers {
		if _, ok := wantedPorts[p]; !ok {
			as.cancel()
			delete(tm.currentAPIServers, p)
		}
	}
	for p := range wantedPorts {
		if _, ok := tm.currentAPIServers[p]; !ok {
			tm.newAPIServerForPort(ctx, p)
		}
	}
	for id := range tm.currentMatchers {
		if _, ok := wantedMatchers[id]; !ok {
			delete(tm.currentMatchers, id)
		}
	}
	for id, ic := range wantedMatchers {
		if _, ok := tm.currentMatchers[id]; !ok {
			tm.newMatcher(ctx, ic)
		}
	}
}

func (tm *TrafficManager) newAPIServerForPort(ctx context.Context, port int) {
	s := restapi.NewServer(tm)
	as := apiServer{Server: s}
	ctx, as.cancel = context.WithCancel(ctx)
	if tm.currentAPIServers == nil {
		tm.currentAPIServers = map[int]*apiServer{port: &as}
	} else {
		tm.currentAPIServers[port] = &as
	}
	go func() {
		if err := s.ListenAndServe(ctx, port); err != nil {
			dlog.Error(ctx, err)
		}
	}()
}

func (tm *TrafficManager) newMatcher(ctx context.Context, ic *manager.InterceptInfo) {
	m, err := matcher.NewRequestFromMap(ic.Headers)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	if tm.currentMatchers == nil {
		tm.currentMatchers = make(map[string]*apiMatcher)
	}
	tm.currentMatchers[ic.Id] = &apiMatcher{
		requestMatcher: m,
		metadata:       ic.Metadata,
	}
}

func (tm *TrafficManager) InterceptInfo(ctx context.Context, callerID, path string, _ uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	tm.currentInterceptsLock.Lock()
	defer tm.currentInterceptsLock.Unlock()

	r := &restapi.InterceptInfo{ClientSide: true}
	am := tm.currentMatchers[callerID]
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

// AddLocalOnlyIntercept adds a local-only intercept
func (tm *TrafficManager) AddLocalOnlyIntercept(c context.Context, spec *manager.InterceptSpec) (*rpc.InterceptResult, error) {
	tm.insLock.Lock()
	if tm.localInterceptedNamespaces == nil {
		tm.localInterceptedNamespaces = map[string]struct{}{}
	}
	tm.localIntercepts[spec.Name] = spec.Namespace
	_, found := tm.interceptedNamespaces[spec.Namespace]
	if !found {
		_, found = tm.localInterceptedNamespaces[spec.Namespace]
	}
	tm.localInterceptedNamespaces[spec.Namespace] = struct{}{}
	tm.insLock.Unlock()
	if !found {
		tm.updateDaemonNamespaces(c)
	}
	return &rpc.InterceptResult{
		InterceptInfo: &manager.InterceptInfo{
			Spec:              spec,
			Disposition:       manager.InterceptDispositionType_ACTIVE,
			MechanismArgsDesc: "as local-only",
			ClientSession:     tm.sessionInfo,
		},
	}, nil
}

func (tm *TrafficManager) RemoveLocalOnlyIntercept(c context.Context, name, namespace string) error {
	dlog.Debugf(c, "removing local-only intercept %s", name)
	delete(tm.localIntercepts, name)
	for _, otherNs := range tm.localIntercepts {
		if otherNs == namespace {
			return nil
		}
	}

	// Ensure that namespace is removed from localInterceptedNamespaces if this was the last local intercept
	// for the given namespace.
	tm.insLock.Lock()
	delete(tm.localInterceptedNamespaces, namespace)
	tm.insLock.Unlock()
	tm.updateDaemonNamespaces(c)
	return nil
}
