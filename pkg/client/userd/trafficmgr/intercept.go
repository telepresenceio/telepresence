package trafficmgr

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
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

	// finalRemovalDone is closed when the traffic-manager sends a snapshot that no longer contains
	// this intercept.
	finalRemovalDone chan struct{}
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

func (ic *intercept) podAccess() *podAccess {
	return &podAccess{
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
		wg:               &ic.wg,
	}
}

func (s *session) watchInterceptsHandler(ctx context.Context) error {
	return runWithRetry(ctx, s.watchInterceptsLoop)
}

func (s *session) watchInterceptsLoop(ctx context.Context) error {
	pat := newPodAccessTracker()
	snapMap := make(map[string]*manager.InterceptInfo)
	err := watcher.WatchWithRetry(ctx, "WatchInterceptsDelta", client.GetConfig(ctx).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.InterceptInfoDelta], error) {
			return s.ManagerClient().WatchInterceptsDelta(s, s.SessionInfo())
		},
		func(delta *manager.InterceptInfoDelta) error {
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			s.handleInterceptSnapshot(pat, maps.Values(snapMap))
			return nil
		}, func() error {
			clear(snapMap)
			return s.reconnectManager()
		})
	if err != nil && status.Code(err) == codes.Unimplemented {
		// Fall back to streaming all intercepts if the traffic manager doesn't support delta updates.'
		clog.Warnf(ctx, "WatchInterceptsDelta is not implemented by the traffic-manager, falling back to WatchIntercepts and full snapshots")
		err = watcher.WatchWithRetry(ctx, "WatchIntercepts", client.GetConfig(ctx).Grpc().WatchRetryInterval,
			func(ctx context.Context) (grpc.ServerStreamingClient[manager.InterceptInfoSnapshot], error) {
				return s.ManagerClient().WatchIntercepts(s, s.SessionInfo())
			},
			func(snapshot *manager.InterceptInfoSnapshot) error {
				s.handleInterceptSnapshot(pat, snapshot.Intercepts)
				return nil
			}, s.reconnectManager)
	}
	// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are cancelled correctly.
	s.handleInterceptSnapshot(pat, nil)
	return err
}

func (s *session) handleInterceptSnapshot(pat *podAccessTracker, intercepts []*manager.InterceptInfo) {
	s.setCurrentIntercepts(intercepts)
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

		pa := ic.podAccess()
		var err error
		if ii.Disposition == manager.InterceptDispositionType_ACTIVE {
			ns := ii.Spec.Namespace
			if s.Namespace != ns {
				err = errcat.User.Newf("active intercepts in both namespace %s and %s", ns, s.Namespace)
			} else {
				err = pa.ensureAccess(ic.ctx, s.rootDaemon)
			}
		} else {
			err = fmt.Errorf("intercept in error state %v: %v", ii.Disposition, ii.Message)
		}

		// Notify waiters for active intercepts
		if aw != nil {
			clog.Debugf(s, "wait status: intercept id=%q is no longer WAITING; is now %v", ii.Id, ii.Disposition)
			ir := interceptResult{
				intercept: ic,
				err:       err,
			}
			if err == nil {
				ir.mountsDone = pat.getOrCreateMountsDone(pa)
			} else {
				md := make(chan struct{})
				close(md)
				ir.mountsDone = md
			}
			select {
			case aw.waitCh <- ir:
				if err != nil {
					// Error logged by receiver
					continue
				}
			default:
				// Channel was closed
				clog.Debugf(s, "unable to propagate intercept id=%q", ii.Id)
			}
		}
		if err != nil {
			clog.Error(s, err)
			continue
		}

		if s.isPodDaemon {
			// disable mount point logic
			pa.ftpPort = 0
			pa.sftpPort = 0
		}
		pat.start(pa)
	}
	pat.cancelUnwanted(s)
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

func (s *session) setCurrentIntercepts(iis []*manager.InterceptInfo) {
	s.currentInterceptsLock.Lock()
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
			ic = &intercept{InterceptInfo: ii, finalRemovalDone: make(chan struct{})}
			ic.ctx, ic.cancel = context.WithCancel(s)
			clog.Debugf(s, "Received new intercept %s", ic.Spec.Name)
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
	clog.Debugf(s, "setCurrentIntercepts(%s)", sb.String())

	// Cancel those that no longer exists
	var removed []*intercept
	for id, ic := range s.currentIntercepts {
		if _, ok := intercepts[id]; !ok {
			removed = append(removed, ic)
		}
	}
	s.currentIntercepts = intercepts
	s.reconcileAPIServers()
	s.currentInterceptsLock.Unlock()

	for _, ic := range removed {
		clog.Debugf(s, "Cancelling context for intercept %s", ic.Spec.Name)
		ic.cancel()
		close(ic.finalRemovalDone)
	}
}

type interceptInfo struct {
	// Information provided by the traffic manager as response to the PrepareIntercept call
	preparedIntercept *manager.PreparedIntercept
}

func (s *interceptInfo) PortIdentifier() (types.PortIdentifier, error) {
	var spi string
	if s.preparedIntercept.ServicePortName == "" {
		spi = strconv.Itoa(int(s.preparedIntercept.ServicePort))
	} else {
		spi = s.preparedIntercept.ServicePortName
	}
	return types.NewPortIdentifier(types.FromK8sProtocol(core.Protocol(s.preparedIntercept.Protocol)), spi)
}

func (s *interceptInfo) PreparedIntercept() *manager.PreparedIntercept {
	return s.preparedIntercept
}

func (s *session) ensureNoInterceptConflict(ir *rpc.CreateInterceptRequest) error {
	err := s.ensureNoMountConflict(ir.MountPoint, ir.LocalMountPort)
	if err != nil {
		return err
	}
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	spec := ir.Spec
	for _, iCept := range s.currentIntercepts {
		if iCept.Spec.Name == spec.Name {
			return status.Errorf(codes.AlreadyExists, "intercept with name %q already exists", spec.Name)
		}
	}
	return nil
}

// allBusyLocalPorts returns the sum of all ports that the intercept forwards to and all ports
// that are forwarded from.
func allBusyLocalPorts(targetHost netip.Addr, spec *manager.InterceptSpec) ([]types.AddrPortProto, error) {
	targetPort := spec.TargetPort
	if targetPort == 0 {
		targetPort = spec.ContainerPort
	}
	proto, err := types.ParseProto(spec.Protocol)
	if err != nil {
		return nil, err
	}
	ports := make([]types.AddrPortProto, 0, len(spec.LocalPorts)+len(spec.PodPorts)+1)
	ports = append(ports, types.AddrPortProto{
		AddrPort: netip.AddrPortFrom(targetHost, uint16(targetPort)),
		Proto:    proto,
	})
	for _, lp := range spec.LocalPorts {
		pp, _ := types.ParsePortAndProto(lp)
		ports = append(ports, types.AddrPortProto{
			AddrPort: netip.AddrPortFrom(targetHost, pp.Port),
			Proto:    pp.Proto,
		})
	}
	for _, ps := range spec.PodPorts {
		pm := types.PortMapping(ps)
		pp := pm.ToAsNumeric()
		ports = append(ports, types.AddrPortProto{
			AddrPort: netip.AddrPortFrom(targetHost, pp.Port),
			Proto:    pp.Proto,
		})
	}
	return ports, nil
}

// ensureUniqueLocalPorts returns the sum of all local ports that the intercept will forward to, and all
// local ports that the client will forward from. Also ensures that there are no conflicts among those ports.
// The cluster-side of the port mappings are not checked here because we rely on the PrepareIntercept
// call to already have done that.
func ensureUniqueLocalPorts(targetHost netip.Addr, spec *manager.InterceptSpec, pi *manager.PreparedIntercept) (map[types.AddrPortProto]struct{}, error) {
	targetPort := spec.TargetPort
	if targetPort == 0 {
		targetPort = pi.ContainerPort
	}

	proto, err := types.ParseProto(pi.Protocol)
	if err != nil {
		return nil, err
	}
	ports := make(map[types.AddrPortProto]struct{}, len(spec.LocalPorts)+len(pi.PodPorts)+1)
	ports[types.AddrPortProto{
		AddrPort: netip.AddrPortFrom(targetHost, uint16(targetPort)),
		Proto:    proto,
	}] = struct{}{}

	for _, lp := range spec.LocalPorts {
		pp, err := types.ParsePortAndProto(lp)
		if err != nil {
			return nil, err
		}
		ap := types.AddrPortProto{
			AddrPort: netip.AddrPortFrom(targetHost, pp.Port),
			Proto:    pp.Proto,
		}
		if _, ok := ports[ap]; ok {
			return nil, fmt.Errorf("multiple use of port %s on %s", &pp, spec.TargetHost)
		}
		ports[ap] = struct{}{}
	}
	for _, ps := range pi.PodPorts {
		pm := types.PortMapping(ps)
		if err := pm.Validate(); err != nil {
			return nil, err
		}
		pp := pm.ToAsNumeric()
		ap := types.AddrPortProto{
			AddrPort: netip.AddrPortFrom(targetHost, pp.Port),
			Proto:    pp.Proto,
		}
		if _, ok := ports[ap]; ok {
			return nil, fmt.Errorf("multiple use of port %s on %s", &ap, spec.TargetHost)
		}
		ports[ap] = struct{}{}
	}
	return ports, nil
}

func (s *session) ensureNoPortConflict(spec *manager.InterceptSpec, ir *manager.PreparedIntercept) error {
	targetHost, err := netip.ParseAddr(spec.TargetHost)
	if err != nil {
		return errcat.User.Errorf(err, "invalid target host")
	}
	ports, err := ensureUniqueLocalPorts(targetHost, spec, ir)
	if err != nil {
		return errcat.User.New(err)
	}

	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	for _, ci := range s.currentIntercepts {
		ciSpec := ci.Spec
		targetHost, err = netip.ParseAddr(ciSpec.TargetHost)
		if err != nil {
			return errcat.User.Errorf(err, "invalid target host")
		}
		busyPorts, err := allBusyLocalPorts(targetHost, ciSpec)
		if err != nil {
			return errcat.User.New(err)
		}
		for _, blp := range busyPorts {
			if _, ok := ports[blp]; ok {
				return &tpGrpc.StructuredError{
					Message:  fmt.Sprintf("port %s is already in use by intercept %s", blp, ciSpec.Name),
					Category: errcat.User,
					Code:     codes.AlreadyExists,
				}
			}
		}
	}
	return nil
}

func (s *session) compareFinalizedManagerVersion(major, minor, patch uint64) int {
	mv := s.managerVersion
	n := mv.Major - major
	if n == 0 {
		if n = mv.Minor - minor; n == 0 {
			n = mv.Patch - patch
		}
	}
	return int(n)
}

// CanIntercept checks if it is possible to create an intercept for the given request. The intercept can proceed
// only if the returned rpc.InterceptResult is nil. The returned runtime.Object is either nil, indicating a local
// intercept, or the workload for the intercept.
func (s *session) CanIntercept(ctx context.Context, ir *rpc.CreateInterceptRequest) (userd.InterceptInfo, error) {
	spec := ir.Spec
	if spec.Namespace == "" {
		spec.Namespace = s.Namespace
	} else if s.Namespace != spec.Namespace {
		return nil, errcat.User.Newf("attempt to intercept in namespace %q. Only the connected %q namespace can be intercepted", spec.Namespace, s.Namespace)
	}

	if er := s.ensureNoInterceptConflict(ir); er != nil {
		return nil, er
	}

	if spec.Wiretap && s.compareFinalizedManagerVersion(2, 23, 0) < 0 {
		return nil, errcat.User.Newf("traffic-manager version %s has no support for wiretaps", s.managerVersion)
	}

	if (spec.PortIdentifier == "all" || len(spec.PodPorts) > 0) && s.compareFinalizedManagerVersion(2, 22, 0) < 0 {
		return nil, errcat.User.Newf("traffic-manager version %s has no support for multi-port intercepts", s.managerVersion)
	}

	_, err := netip.ParseAddr(spec.TargetHost)
	if err != nil {
		// The targetHost is not a valid IP. Treat it as a name and create a synthetic IP for it.
		rndIP, err := uuid.NewRandom()
		if err != nil {
			return nil, fmt.Errorf("unable to generate synthetic IP: %w", err)
		}
		targetIP := netip.AddrFrom16(rndIP)
		if s.syntheticIPs == nil {
			s.syntheticIPs = make(map[netip.Addr]string)
		}
		clog.Debugf(s, "Replacing target host %s with synthetic IP %s", spec.TargetHost, targetIP)
		s.syntheticIPs[targetIP] = spec.TargetHost
		spec.TargetHost = targetIP.String()
	}

	mgrIr := s.newCreateInterceptRequest(spec)
	timeoutCtx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutIntercept)
	defer cancel()
	var pi *manager.PreparedIntercept
	for retry := 0; retry < 2; retry++ {
		pi, err = s.ManagerClient().PrepareIntercept(timeoutCtx, mgrIr)
		if err == nil {
			break
		}

		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.FailedPrecondition:
				return nil, errcat.User.New(st.Message())
			case codes.NotFound:
				if strings.HasPrefix(st.Message(), "Client session ") {
					// The manager is not aware of this session. This can happen if the manager is restarted and
					// none of our watchers have yet detected and remedied the situation.
					err = s.reconnectManager()
					if err == nil {
						continue
					}
				}
			}
		}
		return nil, err
	}
	if pi.Error != "" {
		return nil, errcat.Category(pi.ErrorCategory).New(pi.Error)
	}
	if er := s.ensureNoPortConflict(spec, pi); er != nil {
		return nil, er
	}

	iInfo := &interceptInfo{preparedIntercept: pi}
	return iInfo, nil
}

func (s *session) Resolve(addr netip.Addr) (netip.Addr, error) {
	if n, ok := s.syntheticIPs[addr]; ok {
		ips, err := net.LookupIP(n)
		if err != nil {
			return addr, err
		}
		if len(ips) == 0 {
			return addr, fmt.Errorf("unable to resolve %s", n)
		}
		addr, _ = netip.AddrFromSlice(ips[0])
	}
	return addr, nil
}

func (s *session) ResolveName(addr netip.Addr) string {
	return s.syntheticIPs[addr]
}

func (s *session) newCreateInterceptRequest(spec *manager.InterceptSpec) *manager.CreateInterceptRequest {
	return &manager.CreateInterceptRequest{
		Session:       s.SessionInfo(),
		InterceptSpec: spec,
	}
}

// AddIntercept adds one intercept.
func (s *session) AddIntercept(ctx context.Context, ir *rpc.CreateInterceptRequest) (*manager.InterceptInfo, error) {
	iInfo, err := s.CanIntercept(ctx, ir)
	if err != nil {
		return nil, err
	}

	spec := ir.Spec
	spec.Client = s.clientID
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	mgrClient := s.ManagerClient()

	// iInfo.preparedIntercept == nil means that we're using an older traffic-manager, incapable
	// of using PrepareIntercept.
	pi := iInfo.PreparedIntercept()

	if spec.PortIdentifier == "all" {
		spec.PortIdentifier = ""
	} else if pi.ServicePort > 0 || pi.ServicePortName != "" {
		// Make spec port identifier unambiguous.
		spec.ServicePortName = pi.ServicePortName
		spec.ServicePort = pi.ServicePort
		pti, err := iInfo.PortIdentifier()
		if err != nil {
			return nil, err
		}
		spec.PortIdentifier = pti.String()
	}
	clog.Debugf(s, "pi.Protocol = %s", pi.Protocol)
	spec.Protocol = pi.Protocol
	spec.ContainerPort = pi.ContainerPort
	spec.ContainerName = pi.ContainerName
	if spec.NoDefaultPort {
		spec.Name = spec.Agent + "/" + pi.ContainerName
	}
	spec.PodPorts = pi.PodPorts
	if spec.TargetPort == 0 {
		spec.TargetPort = pi.ContainerPort
	}

	spec.ServiceUid = pi.ServiceUid
	spec.WorkloadKind = pi.WorkloadKind

	clog.Debugf(s, "creating intercept %s", spec.Name)
	tos := client.GetConfig(ctx).Timeouts()
	spec.RoundtripLatency = int64(tos.Get(client.TimeoutRoundtripLatency)) * 2 // Account for extra hop
	spec.DialTimeout = int64(tos.Get(client.TimeoutEndpointDial))
	c, cancel := tos.TimeoutContext(ctx, client.TimeoutIntercept)
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

	ii, err := mgrClient.CreateIntercept(c, s.newCreateInterceptRequest(spec))
	if err != nil {
		clog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		return nil, err
	}

	clog.Debugf(c, "created intercept %s", ii.Spec.Name)

	success := false
	defer func() {
		if !success {
			clog.Debugf(c, "intercept %s failed to create, will remove...", ii.Spec.Name)
			if removeErr := s.RemoveIntercept(ii.Spec.Name); removeErr != nil {
				clog.Warnf(c, "failed to remove failed intercept %s: %v", ii.Spec.Name, removeErr)
			}
		}
	}()

	// Wait for the intercept to transition from WAITING or NO_AGENT to ACTIVE. This
	// might result in more than one event.
	for {
		select {
		case <-c.Done():
			return nil, fmt.Errorf("failed to establish intercept: %w", client.CheckTimeout(c, c.Err()))
		case wr := <-waitCh:
			if wr.err != nil {
				return nil, fmt.Errorf("failed to establish intercept: %w", wr.err)
			}
			ic := wr.intercept
			ii = ic.InterceptInfo
			if ii.Disposition != manager.InterceptDispositionType_ACTIVE {
				continue
			}
			select {
			case <-c.Done():
				return nil, fmt.Errorf("failed to establish intercept: %w", client.CheckTimeout(c, c.Err()))
			case <-wr.mountsDone:
			}
			err := s.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
				env, err := s.rootDaemon.TranslateEnvIPs(c, &daemon.Environment{Env: ii.Environment})
				if err == nil {
					ii.Environment = env.Env
				}
				return err
			})
			if err != nil {
				return nil, client.CheckTimeout(c, err)
			}
			success = true // Prevent removal in deferred function
			return ii, nil
		}
	}
}

// RemoveIntercept removes one intercept by name.
func (s *session) RemoveIntercept(name string) error {
	clog.Debugf(s, "Removing intercept %s", name)

	// Make an attempt to remove the created intercept using a time limited Context. Our
	// context is already done.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s), 5*time.Second)
	defer cancel()
	ii := s.getInterceptByName(name)
	if ii == nil {
		clog.Debugf(ctx, "Intercept %s was already removed", name)
		return nil
	}
	return s.removeIntercept(ii)
}

func (s *session) removeIntercept(ic *intercept) error {
	name := ic.Spec.Name
	s.stopHandler(name, ic.handlerContainer, ic.pid)

	// Unmount filesystems before telling the manager to remove the intercept
	ic.cancel()
	ic.wg.Wait()

	c := s.Context
	clog.Debugf(c, "telling manager to remove intercept %s", name)
	tos := client.GetConfig(c).Timeouts()
	cc, cancel := tos.TimeoutContext(c, client.TimeoutTrafficManagerAPI)
	defer cancel()
	_, err := s.ManagerClient().RemoveIntercept(cc, &manager.RemoveInterceptRequest2{
		Session: s.SessionInfo(),
		Name:    name,
	})
	if err == nil {
		select {
		case <-c.Done():
		case <-ic.finalRemovalDone:

		// Just in case the traffic-manager dies before it sends a new snapshot to our intercept watcher.
		case <-time.After(tos.Get(client.TimeoutTrafficManagerAPI)):
		}
	}
	return err
}

func (s *session) stopHandler(name, handlerContainer string, pid int) {
	// No use trying to kill processes when using a container-based daemon, unless
	// that daemon runs as a normal user daemon with a separate root daemon.
	// Some users run a standard telepresence client together with ingests/intercepts
	// in one single container.
	c := s.Context
	if !(proc.RunningInContainer() && s.GetService().RootSessionInProcess()) {
		if handlerContainer != "" {
			if err := docker.StopContainer(c, handlerContainer); err != nil {
				// It's possible that the container is stopped externally before we get here. If so,
				// then that's not an error.
				if !strings.Contains(err.Error(), "No such container") {
					clog.Error(c, err)
				}
			}
		} else if pid != 0 {
			p, err := os.FindProcess(pid)
			if err != nil {
				clog.Errorf(c, "unable to find handler for ingest/intercept %s with pid %d", name, pid)
			} else {
				clog.Debugf(c, "terminating interceptor for ingest/intercept %s with pid %d", name, pid)
				_ = proc.Terminate(p)
			}
		}
	}
}

// AddInterceptor associates the given intercept with a running process. This ensures that
// the running process will be signalled when the intercept is removed.
func (s *session) AddInterceptor(id string, ih *rpc.Interceptor) error {
	added := false
	s.currentInterceptsLock.Lock()
	if ci, ok := s.currentIntercepts[id]; ok {
		clog.Debugf(s, "Adding intercept handler for id %s, %v", id, ih)
		ci.pid = int(ih.Pid)
		ci.handlerContainer = ih.ContainerName
		added = true
	} else {
		if parts := strings.Split(id, "/"); len(parts) == 2 {
			if cg, err := s.findIngest(parts[0], parts[1]); err == nil {
				clog.Debugf(s, "Adding ingest handler for id %s, %v", id, ih)
				cg.pid = int(ih.Pid)
				cg.handlerContainer = ih.ContainerName
				added = true
			}
		}
	}
	s.currentInterceptsLock.Unlock()
	if !added {
		return status.Error(codes.NotFound, fmt.Sprintf("no intercept or ingest with id %s", id))
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
			if cg, err := s.findIngest(parts[0], parts[1]); err == nil {
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
func (s *session) getInterceptByName(name string) *intercept {
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()
	for _, ic := range s.currentIntercepts {
		if ic.Spec.Name == name {
			return ic
		}
	}

	if slashIx := strings.IndexByte(name, '/'); slashIx > 0 {
		container := name[slashIx+1:]
		name = name[:slashIx]
		for _, ic := range s.currentIntercepts {
			if ic.Spec.Name == name && container == ic.Spec.ContainerName {
				return ic
			}
		}
		return nil
	}

	// Check if the name uniquely identifies a `replace` by its workload (always uses <workload>/<container>)
	namePfx := name + "/"
	var found *intercept
	for _, ic := range s.currentIntercepts {
		if strings.HasPrefix(ic.Spec.Name, namePfx) {
			if found != nil {
				// Found a second time using prefix, so the prefix isn't unique and hence not valid.
				return nil
			}
			found = ic
		}
	}
	if found != nil {
		// Name is not unique if it also identifies an ingest with the same workload.
		s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
			if key.workload == name {
				found = nil
				return false
			}
			return true
		})
	}
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
func (s *session) ClearIngestsAndIntercepts() error {
	for _, ic := range s.getCurrentIntercepts() {
		clog.Debugf(s, "Clearing intercept %s", ic.Spec.Name)
		err := s.removeIntercept(ic)
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
	}
	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		clog.Debugf(s, "Clearing ingest %s", key)
		s.stopHandler(key.workload+"/"+key.container, ig.handlerContainer, ig.pid)
		return true
	})
	return nil
}

// reconcileAPIServers start/stop API servers as needed based on the TELEPRESENCE_API_PORT environment variable
// of the currently intercepted agent's env.
func (s *session) reconcileAPIServers() {
	wantedPorts := make(map[int]struct{})
	wantedMatchers := make(map[string]*manager.InterceptInfo)

	agentAPIPort := func(ii *manager.InterceptInfo) int {
		is := ii.Spec
		if ps, ok := ii.Environment[agentconfig.EnvAPIPort]; ok {
			port, err := strconv.ParseUint(ps, 10, 16)
			if err == nil {
				return int(port)
			}
			clog.Errorf(s, "unable to parse TELEPRESENCE_API_PORT(%q) to a port number in agent %s.%s: %v", ps, is.Agent, is.Namespace, err)
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
			s.newAPIServerForPort(p)
		}
	}
	for id := range s.currentMatchers {
		if _, ok := wantedMatchers[id]; !ok {
			delete(s.currentMatchers, id)
		}
	}
	for id, ic := range wantedMatchers {
		if _, ok := s.currentMatchers[id]; !ok {
			s.newMatcher(ic)
		}
	}
}

func (s *session) newAPIServerForPort(port int) {
	svr := restapi.NewServer(s)
	ctx, cancel := context.WithCancel(s)
	as := apiServer{Server: svr, cancel: cancel}
	if s.currentAPIServers == nil {
		s.currentAPIServers = map[int]*apiServer{port: &as}
	} else {
		s.currentAPIServers[port] = &as
	}
	go func() {
		if err := svr.ListenAndServe(ctx, port); err != nil {
			clog.Error(ctx, err)
		}
	}()
}

func (s *session) newMatcher(ic *manager.InterceptInfo) {
	m := matcher.NewRequest(ic.Spec.PathFilters, ic.Spec.HeaderFilters)
	if s.currentMatchers == nil {
		s.currentMatchers = make(map[string]*apiMatcher)
	}
	s.currentMatchers[ic.Id] = &apiMatcher{
		requestMatcher: m,
		metadata:       ic.Spec.Metadata,
	}
}

func (s *session) InterceptInfo(_ context.Context, callerID, path string, _ uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	s.currentInterceptsLock.Lock()
	defer s.currentInterceptsLock.Unlock()

	r := &restapi.InterceptInfo{ClientSide: true}
	am := s.currentMatchers[callerID]
	switch {
	case am == nil:
		clog.Debugf(s, "no matcher found for callerID %s", callerID)
	case am.requestMatcher.MatchesPathAndHeader(path, headers):
		clog.Debugf(s, "%s: matcher %s matches path %q and headers %s", callerID, am.requestMatcher, path, matcher.HeaderStringer(headers))
		r.Intercepted = true
		r.Metadata = am.metadata
	default:
		clog.Debugf(s, "%s: matcher %s does not matches path %q and headers %s", callerID, am.requestMatcher, path, matcher.HeaderStringer(headers))
	}
	return r, nil
}
