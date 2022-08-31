package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	core "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
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

type forwardKey struct {
	Name  string
	PodIP string
}

// The liveIntercept provides synchronization for cancellation of port forwards and mounts.
// This is necessary because a volume mount process must terminate before the corresponding
// file system is removed. The removal cannot take place when the process ends because there
// may be subsequent processes that use the same volume mount during the lifetime of an
// intercept (since an intercept may change pods).
type liveIntercept struct {
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

type liveIntercepts struct {
	// live contains a map of the currently alive port forwards
	live map[forwardKey]*liveIntercept

	// snapshot is recreated for each new intercept snapshot read from the manager
	snapshot map[forwardKey]struct{}
}

func newPortForwards() *liveIntercepts {
	return &liveIntercepts{live: make(map[forwardKey]*liveIntercept)}
}

// start a port forward for the given intercept and remembers that it's alive
func (lpf liveIntercepts) start(ctx context.Context, tm *TrafficManager, ii *manager.InterceptInfo) {
	fk := forwardKey{
		Name:  ii.Spec.Name,
		PodIP: ii.PodIp,
	}

	// Older versions use ii.extraPorts (TCP only), newer versions use ii.localPorts.
	if len(ii.Spec.LocalPorts) == 0 {
		for _, ep := range ii.Spec.ExtraPorts {
			ii.Spec.LocalPorts = append(ii.Spec.LocalPorts, strconv.Itoa(int(ep)))
		}
	}

	if tm.shouldForward(ii) || tm.shouldMount(ii) {
		lpf.snapshot[fk] = struct{}{}
		if _, isLive := lpf.live[fk]; !isLive {
			pfCtx, pfCancel := context.WithCancel(ctx)
			livePortForward := &liveIntercept{cancel: pfCancel}
			tm.startMount(pfCtx, &livePortForward.wg, fk, ii.SftpPort, ii.MountPoint)
			tm.startForwards(pfCtx, &livePortForward.wg, fk, ii.Spec.LocalPorts)
			dlog.Debugf(ctx, "Started forward for %+v", fk)
			lpf.live[fk] = livePortForward
		}
	}
}

// initSnapshot prepares this instance for a new round of start calls followed by a cancelUnwanted
func (lpf *liveIntercepts) initSnapshot() {
	lpf.snapshot = make(map[forwardKey]struct{})
}

// cancelUnwanted cancels all port forwards that hasn't been started since initSnapshot
func (lpf liveIntercepts) cancelUnwanted(ctx context.Context) {
	for fk, lp := range lpf.live {
		if _, isWanted := lpf.snapshot[fk]; !isWanted {
			dlog.Infof(ctx, "Terminating forwards for %s", fk.PodIP)
			lp.cancel()
			delete(lpf.live, fk)
			lp.wg.Wait()
		}
	}
}

// reconcileMountPoints deletes mount points for which there no longer is an intercept
func (tm *TrafficManager) reconcileMountPoints(ctx context.Context, existingIntercepts map[string]struct{}) {
	var mountsToDelete []any
	tm.mountPoints.Range(func(key, value any) bool {
		if _, ok := existingIntercepts[value.(string)]; !ok {
			mountsToDelete = append(mountsToDelete, key)
		}
		return true
	})

	for _, key := range mountsToDelete {
		if _, loaded := tm.mountPoints.LoadAndDelete(key); loaded {
			// Execute the removal in a separate go-routine so that we don't hang the daemon in case
			// the removal hangs on a "resource busy".
			go func(mountPoint string) {
				if runtime.GOOS == "darwin" {
					//  macFUSE will sometimes not unmount in a timely manner so we do this to avoid "resource busy" and
					//  "Device not configured" errors.
					_ = proc.CommandContext(ctx, "umount", mountPoint).Run()
				}
				err := os.Remove(mountPoint)
				switch {
				case err == nil:
					dlog.Infof(ctx, "Removed file system mount %q", mountPoint)
				case os.IsNotExist(err):
					dlog.Infof(ctx, "File system mount %q no longer exists", mountPoint)
				default:
					dlog.Errorf(ctx, "Failed to remove mount point %q: %v", mountPoint, err)
				}
			}(key.(string))
		}
	}
}

func (tm *TrafficManager) workerPortForwardIntercepts(ctx context.Context) error { //nolint:gocognit // bugger off
	// Don't use a dgroup.Group because:
	//  1. we don't actually care about tracking errors (we just always retry) or any of
	//     dgroup's other functionality
	//  2. because goroutines may churn as intercepts are created and deleted, tracking all of
	//     their exit statuses is just a memory leak
	//  3. because we want a per-worker cancel, we'd have to implement our own Context
	//     management on top anyway, so dgroup wouldn't actually save us any complexity.
	portForwards := newPortForwards()
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
		if err != nil {
			err = fmt.Errorf("manager.WatchIntercepts dial: %w", err)
		}
		for err == nil && ctx.Err() == nil {
			var snapshot *manager.InterceptInfoSnapshot
			snapshot, err = stream.Recv()
			var intercepts []*manager.InterceptInfo

			if err != nil {
				if ctx.Err() == nil {
					if !errors.Is(err, io.EOF) {
						err = fmt.Errorf("manager.WatchIntercepts recv: %w", err)
					}
					break
				}
				// context is cancelled. Continue as if we had an empty snapshot. This
				// will ensure that volume mounts are cancelled correctly.
			} else {
				intercepts = snapshot.Intercepts
			}
			tm.setCurrentIntercepts(ctx, intercepts)

			// allNames contains the names of all intercepts, irrespective of their status
			allNames := make(map[string]struct{})

			portForwards.initSnapshot()
			namespaces := make(map[string]struct{})
			for _, intercept := range intercepts {
				allNames[intercept.Spec.Name] = struct{}{}

				var iceptError error
				switch intercept.Disposition {
				case manager.InterceptDispositionType_ACTIVE:
					// do nothing
				case manager.InterceptDispositionType_WAITING:
					continue
				default:
					iceptError = fmt.Errorf("intercept in error state %v: %v", intercept.Disposition, intercept.Message)
				}

				// Notify waiters for active intercepts
				if chUt, loaded := tm.activeInterceptsWaiters.Load(intercept.Spec.Name); loaded {
					if ch, ok := chUt.(chan interceptResult); ok {
						dlog.Debugf(ctx, "wait status: intercept id=%q is no longer WAITING; is now %v", intercept.Id, intercept.Disposition)
						ir := interceptResult{
							intercept: intercept,
							err:       iceptError,
						}
						select {
						case ch <- ir:
						default:
							// Channel was closed
						}
					}
				}
				if iceptError == nil {
					namespaces[intercept.Spec.Namespace] = struct{}{}
					if tm.isPodDaemon {
						intercept.SftpPort = 0 // disable mount point logic
					}
					portForwards.start(ctx, tm, intercept)
				}
			}
			portForwards.cancelUnwanted(ctx)
			tm.reconcileMountPoints(ctx, allNames)
			if ctx.Err() == nil && !tm.isPodDaemon {
				tm.setInterceptedNamespaces(ctx, namespaces)
			}
		}

		if ctx.Err() == nil {
			dlog.Errorf(ctx, "reading port-forwards from manager: %v", err)
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
		}
	}
	return nil
}

// getCurrentIntercepts returns a copy of the current intercept snapshot amended with
// the local filesystem mount point.
func (tm *TrafficManager) getCurrentIntercepts() []*manager.InterceptInfo {
	// Copy the current snapshot
	tm.currentInterceptsLock.Lock()
	intercepts := make([]*manager.InterceptInfo, len(tm.currentIntercepts))
	for i, ii := range tm.currentIntercepts {
		intercepts[i] = proto.Clone(ii).(*manager.InterceptInfo)
	}
	tm.currentInterceptsLock.Unlock()

	// Amend with local info
	for _, ii := range intercepts {
		ii.ClientMountPoint = tm.mountPointForIntercept(ii.Spec.Name)
	}
	return intercepts
}

func (tm *TrafficManager) setCurrentIntercepts(ctx context.Context, intercepts []*manager.InterceptInfo) {
	tm.currentInterceptsLock.Lock()
	tm.currentIntercepts = intercepts
	tm.reconcileAPIServers(ctx)
	tm.currentInterceptsLock.Unlock()
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

	for _, iCept := range tm.getCurrentIntercepts() {
		if iCept.Spec.Name == spec.Name {
			return nil, interceptError(common.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
		}
		if iCept.Spec.TargetPort == spec.TargetPort && iCept.Spec.TargetHost == spec.TargetHost {
			return nil, &rpc.InterceptResult{
				Error:         common.InterceptError_LOCAL_TARGET_IN_USE,
				ErrorText:     spec.Name,
				ErrorCategory: int32(errcat.User),
				InterceptInfo: iCept,
			}
		}
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

	deleteMount := false
	if !ir.IsPodDaemon {
		if ir.MountPoint != "" {
			// Ensure that the mount-point is free to use
			if prev, loaded := tm.mountPoints.LoadOrStore(ir.MountPoint, spec.Name); loaded {
				return interceptError(common.InterceptError_MOUNT_POINT_BUSY, errcat.User.Newf(prev.(string))), nil
			}

			// Assume that the mount-point should to be removed from the busy map. Only a happy path
			// to successful intercept that actually has remote mounts will set this to false.
			deleteMount = true
			defer func() {
				if deleteMount {
					tm.mountPoints.Delete(ir.MountPoint)
				}
			}()
		}
	}

	dlog.Debugf(c, "creating intercept %s", spec.Name)
	tos := &client.GetConfig(c).Timeouts
	spec.RoundtripLatency = int64(tos.Get(client.TimeoutRoundtripLatency)) * 2 // Account for extra hop
	spec.DialTimeout = int64(tos.Get(client.TimeoutEndpointDial))
	c, cancel := tos.TimeoutContext(c, client.TimeoutIntercept)
	defer cancel()

	// The agent is in place and the traffic-manager has acknowledged the creation of the intercept. It
	// should become active within a few seconds.
	waitCh := make(chan interceptResult, 2) // Need a buffer because reply can come before we're reading the channel
	tm.activeInterceptsWaiters.Store(spec.Name, waitCh)
	defer func() {
		if wc, loaded := tm.activeInterceptsWaiters.LoadAndDelete(spec.Name); loaded {
			close(wc.(chan interceptResult))
		}
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
			ii = wr.intercept
			if ii.Disposition != manager.InterceptDispositionType_ACTIVE {
				continue
			}
			// Older traffic-managers pass env in the agent info
			if agentEnv != nil {
				ii.Environment = agentEnv
			}
			result.InterceptInfo = ii
			if !ir.IsPodDaemon {
				mountPoint := tm.mountPointForIntercept(ii.Spec.Name)
				if mountPoint != "" && ii.SftpPort > 0 {
					deleteMount = false // Mount-point is busy until intercept ends
					ii.ClientMountPoint = mountPoint
				}
			}
			success = true
			return result, nil
		}
	}
}

// shouldForward returns true if the intercept info given should result in ports being forwarded
func (tm *TrafficManager) shouldForward(ii *manager.InterceptInfo) bool {
	return len(ii.Spec.LocalPorts) > 0
}

type portForward struct {
	forwardKey
	Port string
}

// startForwards starts port forwards and mounts for the given forwardKey.
// It assumes that the user has called shouldForward and is sure that something will be started.
func (tm *TrafficManager) startForwards(ctx context.Context, wg *sync.WaitGroup, fk forwardKey, localPorts []string) {
	for _, port := range localPorts {
		pfCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%s", fk.PodIP, port))
		wg.Add(1)
		go tm.workerPortForwardIntercept(pfCtx, portForward{fk, port}, wg)
	}
}

func (tm *TrafficManager) workerPortForwardIntercept(ctx context.Context, pf portForward, wg *sync.WaitGroup) {
	defer wg.Done()
	pp, err := agentconfig.NewPortAndProto(pf.Port)
	if err != nil {
		dlog.Errorf(ctx, "malformed extra port %q: %v", pf.Port, err)
		return
	}
	addr, err := pp.Addr()
	if err != nil {
		dlog.Errorf(ctx, "unable to resolve extra port %q: %v", pf.Port, err)
		return
	}
	f := forwarder.NewInterceptor(addr, pf.PodIP, pp.Port)
	err = f.Serve(ctx, nil)
	if err != nil && ctx.Err() == nil {
		dlog.Errorf(ctx, "port-forwarder failed with %v", err)
	}
}

// RemoveIntercept removes one intercept by name
func (tm *TrafficManager) RemoveIntercept(c context.Context, name string) error {
	dlog.Debugf(c, "Removing intercept %s", name)

	if ns, ok := tm.localIntercepts[name]; ok {
		return tm.RemoveLocalOnlyIntercept(c, name, ns)
	}

	var ii *manager.InterceptInfo
	for _, cept := range tm.getCurrentIntercepts() {
		if cept.Spec.Name == name {
			ii = cept
			break
		}
	}

	if ii == nil {
		dlog.Debugf(c, "Intercept %s was already removed", name)
		return nil
	}
	return tm.removeIntercept(c, ii)
}

func (tm *TrafficManager) removeIntercept(c context.Context, ii *manager.InterceptInfo) error {
	tm.currentInterceptsLock.Lock()
	pid, ok := tm.currentInterceptors[ii.Id]
	if ok {
		delete(tm.currentInterceptors, ii.Id)
	}
	tm.currentInterceptsLock.Unlock()
	name := ii.Spec.Name
	if ok {
		p, err := os.FindProcess(pid)
		if err != nil {
			dlog.Errorf(c, "unable to find interceptor for intercept %s with pid %d", name, pid)
		} else {
			dlog.Debugf(c, "terminating interceptor for intercept %s with pid %d", name, pid)
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
	tm.currentInterceptors[s] = i
	tm.currentInterceptsLock.Unlock()
	return nil
}

func (tm *TrafficManager) RemoveInterceptor(s string) error {
	tm.currentInterceptsLock.Lock()
	delete(tm.currentInterceptors, s)
	tm.currentInterceptsLock.Unlock()
	return nil
}

// GetInterceptSpec returns the InterceptSpec for the given name, or nil if no such spec exists
func (tm *TrafficManager) GetInterceptSpec(name string) *manager.InterceptSpec {
	if ns, ok := tm.localIntercepts[name]; ok {
		return &manager.InterceptSpec{Name: name, Namespace: ns, WorkloadKind: "local"}
	}
	for _, cept := range tm.getCurrentIntercepts() {
		if cept.Spec.Name == name {
			return cept.Spec
		}
	}
	return nil
}

// InterceptsForWorkload returns the client's current intercepts on the given namespace and workload combination
func (tm *TrafficManager) InterceptsForWorkload(workloadName, namespace string) []*manager.InterceptSpec {
	wlis := make([]*manager.InterceptSpec, 0)
	for _, cept := range tm.getCurrentIntercepts() {
		if cept.Spec.Agent == workloadName && cept.Spec.Namespace == namespace {
			wlis = append(wlis, cept.Spec)
		}
	}
	return wlis
}

// ClearIntercepts removes all intercepts
func (tm *TrafficManager) ClearIntercepts(c context.Context) error {
	for _, cept := range tm.getCurrentIntercepts() {
		dlog.Debugf(c, "Clearing intercept %s", cept.Spec.Name)
		err := tm.removeIntercept(c, cept)
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
		if ic.Disposition == manager.InterceptDispositionType_ACTIVE {
			if port := agentAPIPort(ic); port > 0 {
				wantedPorts[port] = struct{}{}
				wantedMatchers[ic.Id] = ic
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
