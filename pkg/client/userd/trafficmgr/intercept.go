package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
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
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type forwardKey struct {
	Name  string
	PodIP string
}

type mountForward struct {
	forwardKey
	SftpPort         int32
	RemoteMountPoint string
}

type portForward struct {
	forwardKey
	Port int32
}

// The livePortForward struct provides synchronization for cancellation of port forwards.
// This is necessary because a volume mount process must terminate before the corresponding
// file system is removed. The removal cannot take place when the process ends because there
// may be subsequent processes that use the same volume mount during the lifetime of an
// intercept (since an intercept may change pods).
type livePortForward struct {
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

type livePortForwards struct {
	// live contains a map of the currently alive port forwards
	live map[forwardKey]*livePortForward

	// snapshot is recreated for each new intercept snapshot read from the manager
	snapshot map[forwardKey]struct{}
}

func newPortForwards() *livePortForwards {
	return &livePortForwards{live: make(map[forwardKey]*livePortForward)}
}

// start a port forward for the given intercept and remembers that it's alive
func (lpf livePortForwards) start(ctx context.Context, tm *TrafficManager, ii *manager.InterceptInfo) {
	fk := forwardKey{
		Name:  ii.Spec.Name,
		PodIP: ii.PodIp,
	}
	if tm.shouldForward(ii) {
		lpf.snapshot[fk] = struct{}{}
		if _, isLive := lpf.live[fk]; !isLive {
			pfCtx, pfCancel := context.WithCancel(ctx)
			livePortForward := &livePortForward{cancel: pfCancel}
			tm.startForwards(pfCtx, &livePortForward.wg, fk, ii.SftpPort, ii.MountPoint, ii.Spec.ExtraPorts)
			dlog.Debugf(ctx, "Started forward for %+v", fk)
			lpf.live[fk] = livePortForward
		}
	}
}

// initSnapshot prepares this instance for a new round of start calls followed by a cancelUnwanted
func (lpf *livePortForwards) initSnapshot() {
	lpf.snapshot = make(map[forwardKey]struct{})
}

// cancelUnwanted cancels all port forwards that hasn't been started since initSnapshot
func (lpf livePortForwards) cancelUnwanted(ctx context.Context) {
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
	var mountsToDelete []interface{}
	tm.mountPoints.Range(func(key, value interface{}) bool {
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
					_ = dexec.CommandContext(ctx, "umount", mountPoint).Run()
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

func (tm *TrafficManager) workerPortForwardIntercepts(ctx context.Context) error {
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
					portForwards.start(ctx, tm, intercept)
				}
			}
			portForwards.cancelUnwanted(ctx)
			tm.reconcileMountPoints(ctx, allNames)
			if ctx.Err() == nil {
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
		ii := ii // Pin it
		tm.mountPoints.Range(func(k, v interface{}) bool {
			if v.(string) == ii.Spec.Name {
				ii.ClientMountPoint = k.(string)
				return false
			}
			return true
		})
	}
	return intercepts
}

func (tm *TrafficManager) setCurrentIntercepts(ctx context.Context, intercepts []*manager.InterceptInfo) {
	tm.currentInterceptsLock.Lock()
	tm.currentIntercepts = intercepts
	tm.reconcileAPIServers(ctx)
	tm.currentInterceptsLock.Unlock()
}

func interceptError(tp rpc.InterceptError, err error) *rpc.InterceptResult {
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

func imageVersion(image string) *semver.Version {
	if cp := strings.LastIndexByte(image, ':'); cp > 0 {
		if v, err := semver.Parse(image[cp+1:]); err == nil {
			return &v
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
		return nil, interceptError(rpc.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(ir.Spec.Agent))
	}

	if _, inUse := tm.localIntercepts[spec.Name]; inUse {
		return nil, interceptError(rpc.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
	}

	for _, iCept := range tm.getCurrentIntercepts() {
		if iCept.Spec.Name == spec.Name {
			return nil, interceptError(rpc.InterceptError_ALREADY_EXISTS, errcat.User.Newf(spec.Name))
		}
		if iCept.Spec.TargetPort == spec.TargetPort && iCept.Spec.TargetHost == spec.TargetHost {
			return nil, &rpc.InterceptResult{
				Error:         rpc.InterceptError_LOCAL_TARGET_IN_USE,
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
		return nil, interceptError(rpc.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}
	if pi.Error != "" {
		return nil, interceptError(rpc.InterceptError_TRAFFIC_MANAGER_ERROR, errcat.Category(pi.ErrorCategory).Newf(pi.Error))
	}

	// Verify that the receiving agent can handle the mechanism arguments that are passed to it.
	if spec.Mechanism == "http" {
		if ir.Spec.MechanismArgs, err = makeFlagsCompatible(imageVersion(pi.AgentImage), ir.Spec.MechanismArgs); err != nil {
			return nil, interceptError(rpc.InterceptError_UNKNOWN_FLAG, err)
		}
		dlog.Debugf(c, "Using %s flags %v", ir.Spec.Mechanism, ir.Spec.MechanismArgs)
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
			return nil, interceptError(rpc.InterceptError_NO_ACCEPTABLE_WORKLOAD, errcat.User.Newf(spec.Name))
		}
		err = fmt.Errorf("failed to get workload %s.%s: %w", spec.Agent, spec.Namespace, err)
		dlog.Error(c, err)
		return nil, interceptError(rpc.InterceptError_TRAFFIC_MANAGER_ERROR, err)
	}

	// Verify that the receiving agent can handle the mechanism arguments that are passed to it.
	if spec.Mechanism == "http" {
		var agentVer *semver.Version
		podTpl := wl.GetPodTemplate()

		// Check if the workload is auto installed. This also verifies annotation consistency
		autoInstall, err := useAutoInstall(podTpl)
		if err != nil {
			return nil, interceptError(rpc.InterceptError_MISCONFIGURED_WORKLOAD, errcat.User.New(err))
		}
		for i := range podTpl.Spec.Containers {
			if ct := &podTpl.Spec.Containers[i]; ct.Name == install.AgentContainerName {
				image := ct.Image
				if autoInstall {
					// Image will be updated to the specified image unless they are equal
					image = ir.AgentImage
				}
				agentVer = imageVersion(image)
				break
			}
		}
		if spec.MechanismArgs, err = makeFlagsCompatible(agentVer, spec.MechanismArgs); err != nil {
			return nil, interceptError(rpc.InterceptError_UNKNOWN_FLAG, err)
		}
		dlog.Debugf(c, "Using %s flags %v", spec.Mechanism, spec.MechanismArgs)
	}

	svcProps, err := exploreSvc(c, spec.ServicePortIdentifier, spec.ServiceName, wl)
	if err != nil {
		return nil, interceptError(rpc.InterceptError_FAILED_TO_ESTABLISH, err)
	}
	svcProps.apiKey = apiKey
	return svcProps, svcProps.interceptResult()
}

func makeFlagsCompatible(agentVer *semver.Version, args []string) ([]string, error) {
	// We get a normalized representation of all flags here, regardless of if they've
	// been set or not, so we start splitting them into flag and value and skipping
	// those that aren't set.
	m := make(map[string][]string, len(args))
	for _, ma := range args {
		if eqi := strings.IndexByte(ma, '='); eqi > 2 && eqi+1 < len(ma) {
			k := ma[2:eqi]
			m[k] = append(m[k], ma[eqi+1:])
		}
	}
	// Concat all --match flags (renamed to --header) with --header flags
	if hs, ok := m["match"]; ok {
		delete(m, "match")
		hs = append(hs, m["header"]...)
		ds := make([]string, 0, len(hs))
		for _, h := range hs {
			if h != "auto" {
				ds = append(ds, h)
			}
		}
		if len(ds) == 0 {
			// restore the default
			ds = append(ds, "auto")
		}
		m["header"] = ds
	}
	if agentVer != nil {
		if agentVer.LE(semver.MustParse("1.11.8")) {
			if hs, ok := m["header"]; ok {
				delete(m, "header")
				m["match"] = hs
			}
			for ma := range m {
				switch ma {
				case "meta", "path-equal", "path-prefix", "path-regex":
					return nil, errcat.User.New("--http-" + ma)
				}
			}
			if agentVer.LE(semver.MustParse("1.11.7")) {
				if pt, ok := m["plaintext"]; ok {
					if len(pt) > 0 && pt[0] == "true" {
						return nil, errcat.User.New("--http-plaintext")
					}
					delete(m, "plaintext")
				}
			}
		}
	}
	args = make([]string, 0, len(args))
	ks := make([]string, len(m))
	i := 0
	for k := range m {
		ks[i] = k
		i++
	}
	sort.Strings(ks)
	for _, k := range ks {
		for _, v := range m[k] {
			args = append(args, "--"+k+"="+v)
		}
	}
	return args, nil
}

// AddIntercept adds one intercept
func (tm *TrafficManager) AddIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	var svcProps *serviceProps
	svcProps, result = tm.CanIntercept(c, ir)
	if result != nil && result.Error != rpc.InterceptError_UNSPECIFIED {
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
	if svcProps.preparedIntercept == nil {
		// It's OK to just call addAgent every time; if the agent is already installed then it's a
		// no-op.
		agentEnv, result = tm.addAgent(c, svcProps, ir.AgentImage, apiPort)
		if result.Error != rpc.InterceptError_UNSPECIFIED {
			return result, nil
		}
	} else {
		// Make spec port identifier unambiguous.
		spec.ServiceName = svcProps.preparedIntercept.ServiceName
		spec.ServicePortIdentifier = svcProps.preparedIntercept.ServicePortName
	}

	spec.ServiceUid = result.ServiceUid
	spec.WorkloadKind = result.WorkloadKind

	deleteMount := false
	if ir.MountPoint != "" {
		// Ensure that the mount-point is free to use
		if prev, loaded := tm.mountPoints.LoadOrStore(ir.MountPoint, spec.Name); loaded {
			return interceptError(rpc.InterceptError_MOUNT_POINT_BUSY, errcat.User.Newf(prev.(string))), nil
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
				return interceptError(rpc.InterceptError_FAILED_TO_ESTABLISH, errcat.User.New(wr.err)), nil
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
			if ir.MountPoint != "" && ii.SftpPort > 0 {
				deleteMount = false // Mount-point is busy until intercept ends
				ii.ClientMountPoint = ir.MountPoint
			}
			success = true
			return result, nil
		}
	}
}

// shouldForward returns true if the intercept info given should result in mounts or ports being forwarded
func (tm *TrafficManager) shouldForward(ii *manager.InterceptInfo) bool {
	return ii.SftpPort > 0 || len(ii.Spec.ExtraPorts) > 0
}

// startForwards starts port forwards and mounts for the given forwardKey.
// It assumes that the user has called shouldForward and is sure that something will be started.
func (tm *TrafficManager) startForwards(ctx context.Context, wg *sync.WaitGroup, fk forwardKey, sftpPort int32, remoteMountPoint string, extraPorts []int32) {
	if sftpPort > 0 {
		// There's nothing to mount if the SftpPort is zero
		mntCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", fk.PodIP, sftpPort))
		wg.Add(1)
		go tm.workerMountForwardIntercept(mntCtx, mountForward{fk, sftpPort, remoteMountPoint}, wg)
	}
	for _, port := range extraPorts {
		pfCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", fk.PodIP, port))
		wg.Add(1)
		go tm.workerPortForwardIntercept(pfCtx, portForward{fk, port}, wg)
	}
}

func (tm *TrafficManager) workerPortForwardIntercept(ctx context.Context, pf portForward, wg *sync.WaitGroup) {
	defer wg.Done()
	// Using kubectl port-forward here would require the pod name to be either fetched from the API server, or threaded
	// all the way through from the intercept request to the agent and into the WatchIntercepts; it would also create
	// additional connections that would have to be recovered in case of failure. Instead, we re-use the forwarder from
	// the agent, and dial the pod's IP directly. This will keep all connections to the cluster going through the TUN
	// device and the existing port-forward to the traffic manager.
	addr := net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: int(pf.Port),
	}
	f := forwarder.NewForwarder(&addr, pf.PodIP, uint16(pf.Port))
	err := f.Serve(ctx)
	if err != nil && ctx.Err() == nil {
		dlog.Errorf(ctx, "port-forwarder failed with %v", err)
	}
}

func (tm *TrafficManager) workerMountForwardIntercept(ctx context.Context, mf mountForward, wg *sync.WaitGroup) {
	defer wg.Done()

	var mountPoint string
	tm.mountPoints.Range(func(key, value interface{}) bool {
		if mf.Name == value.(string) {
			mountPoint = key.(string)
			return false
		}
		return true
	})
	if mountPoint == "" {
		dlog.Errorf(ctx, "No mount point found for intercept %q", mf.Name)
		return
	}

	dlog.Infof(ctx, "Mounting file system for intercept %q at %q", mf.Name, mountPoint)

	// The mounts performed here are synced on by podIP + sftpPort to keep track of active
	// mounts. This is not enough in situations when a pod is deleted and another pod
	// takes over. That is two different IPs so an additional synchronization on the actual
	// mount point is necessary to prevent that it is established and deleted at the same
	// time.
	mountMutex := new(sync.Mutex)
	mountMutex.Lock()
	if oldMutex, loaded := tm.mountMutexes.LoadOrStore(mountPoint, mountMutex); loaded {
		mountMutex.Unlock() // not stored, so unlock and throw away
		mountMutex = oldMutex.(*sync.Mutex)
		mountMutex.Lock()
	}

	defer func() {
		tm.mountMutexes.Delete(mountPoint)
		mountMutex.Unlock()
	}()

	// Retry mount in case it gets disconnected
	err := client.Retry(ctx, "sshfs", func(ctx context.Context) error {
		dl := &net.Dialer{Timeout: 3 * time.Second}
		conn, err := dl.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", mf.PodIP, mf.SftpPort))
		if err != nil {
			return err
		}
		defer conn.Close()
		sshfsArgs := []string{
			"-F", "none", // don't load the user's config file
			"-f", // foreground operation

			// connection settings
			"-C", // compression
			"-oConnectTimeout=10",
			"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
			"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either
			"-o", "slave",                    // Unencrypted via stdin/stdout

			// mount directives
			"-o", "follow_symlinks",
			"-o", "allow_root", // needed to make --docker-run work as docker runs as root
			"localhost:" + mf.RemoteMountPoint, // what to mount
			mountPoint,                         // where to mount it
		}
		exe := "sshfs"
		if runtime.GOOS == "windows" {
			// Use sshfs-win to launch the sshfs
			sshfsArgs = append([]string{"cmd", "-ouid=-1", "-ogid=-1"}, sshfsArgs...)
			exe = "sshfs-win"
		}
		err = dpipe.DPipe(ctx, conn, exe, sshfsArgs...)
		time.Sleep(time.Second)

		// sshfs sometimes leave the mount point in a bad state. This will clean it up
		ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = dexec.CommandContext(ctx, "fusermount", "-uz", mountPoint).Run()
		return err
	}, 3*time.Second, 6*time.Second)

	if err != nil && ctx.Err() == nil {
		dlog.Error(ctx, err)
	}
}

// RemoveIntercept removes one intercept by name
func (tm *TrafficManager) RemoveIntercept(c context.Context, name string) error {
	if ns, ok := tm.localIntercepts[name]; ok {
		return tm.RemoveLocalOnlyIntercept(c, name, ns)
	}
	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	_, err := tm.managerClient.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: tm.session(),
		Name:    name,
	})
	return err
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

// clearIntercepts removes all intercepts
func (tm *TrafficManager) clearIntercepts(c context.Context) error {
	for _, cept := range tm.getCurrentIntercepts() {
		err := tm.RemoveIntercept(c, cept.Spec.Name)
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
		if ps, ok := ii.Environment["TELEPRESENCE_API_PORT"]; ok {
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
