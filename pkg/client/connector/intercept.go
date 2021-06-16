package connector

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/actions"
	"github.com/telepresenceio/telepresence/v2/pkg/dpipe"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

func (s *service) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_UNSPECIFIED
	msg := ""
	switch {
	case s.cluster == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case s.trafficMgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	default:
		select {
		case <-s.trafficMgr.startup:
			if s.trafficMgr.managerClient == nil {
				ie = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
				msg = s.trafficMgr.managerErr.Error()
			}
		default:
			ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
		}
	}
	return ie, msg
}

type forwardKey struct {
	Name  string
	PodIP string
}

type mountForward struct {
	forwardKey
	SftpPort int32
}

type portForward struct {
	forwardKey
	Port int32
}

func (tm *trafficManager) workerPortForwardIntercepts(ctx context.Context) error {
	// Don't use a dgroup.Group because:
	//  1. we don't actually care about tracking errors (we just always retry) or any of
	//     dgroup's other functionality
	//  2. because goroutines may churn as intercepts are created and deleted, tracking all of
	//     their exit statuses is just a memory leak
	//  3. because we want a per-worker cancel, we'd have to implement our own Context
	//     management on top anyway, so dgroup wouldn't actually save us any complexity.
	var wg sync.WaitGroup

	livePortForwards := make(map[forwardKey]context.CancelFunc)

	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		<-tm.startup
		stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
		if err != nil {
			err = fmt.Errorf("manager.WatchIntercepts dial: %w", err)
		}
		for err == nil {
			var snapshot *manager.InterceptInfoSnapshot
			snapshot, err = stream.Recv()
			if err != nil {
				err = fmt.Errorf("manager.WatchIntercepts recv: %w", err)
				break
			}
			snapshotPortForwards := make(map[forwardKey]struct{})
			namespaces := make(map[string]struct{})
			for _, intercept := range snapshot.Intercepts {
				if intercept.Disposition != manager.InterceptDispositionType_ACTIVE {
					continue
				}
				namespaces[intercept.Spec.Namespace] = struct{}{}
				fk := forwardKey{
					Name:  intercept.Spec.Name,
					PodIP: intercept.PodIp,
				}
				if _, isLive := livePortForwards[fk]; !isLive {
					pfCtx, pfCancel := context.WithCancel(ctx)
					forwarded := tm.startForwards(pfCtx, &wg, fk, intercept.SftpPort, intercept.Spec.ExtraPorts)
					if forwarded {
						snapshotPortForwards[fk] = struct{}{}
						livePortForwards[fk] = pfCancel
					} else {
						pfCancel()
					}
				}
			}
			for fk, cancel := range livePortForwards {
				if _, isWanted := snapshotPortForwards[fk]; !isWanted {
					dlog.Infof(ctx, "Terminating forwards for %s", fk.PodIP)
					cancel()
					delete(livePortForwards, fk)
				}
			}
			tm.setInterceptedNamespaces(ctx, namespaces)
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

	wg.Wait()

	return nil
}

// addIntercept adds one intercept
func (tm *trafficManager) addIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	spec := ir.Spec
	spec.Namespace = tm.actualNamespace(spec.Namespace)
	if spec.Namespace == "" {
		// namespace is not currently mapped
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_NO_ACCEPTABLE_WORKLOAD,
			ErrorText: spec.Name,
		}, nil
	}

	if _, inUse := tm.localIntercepts[spec.Name]; inUse {
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_ALREADY_EXISTS,
			ErrorText: spec.Name,
		}, nil
	}

	<-tm.startup
	intercepts, err := actions.ListMyIntercepts(c, tm.managerClient, tm.session().SessionId)
	if err != nil {
		return nil, err
	}
	for _, iCept := range intercepts {
		if iCept.Spec.Name == spec.Name {
			return &rpc.InterceptResult{
				Error:     rpc.InterceptError_ALREADY_EXISTS,
				ErrorText: spec.Name,
			}, nil
		}
		if iCept.Spec.TargetPort == spec.TargetPort && iCept.Spec.TargetHost == spec.TargetHost {
			return &rpc.InterceptResult{
				InterceptInfo: &manager.InterceptInfo{Spec: spec},
				Error:         rpc.InterceptError_LOCAL_TARGET_IN_USE,
				ErrorText:     iCept.Spec.Name,
			}, nil
		}
	}

	if spec.Agent == "" {
		return tm.addLocalOnlyIntercept(c, spec)
	}

	spec.Client = tm.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	// It's OK to just call addAgent every time; if the agent is already installed then it's a
	// no-op.
	var result *rpc.InterceptResult
	if result = tm.addAgent(c, spec.Namespace, spec.Agent, spec.ServiceName, spec.ServicePortIdentifier, ir.AgentImage); result.Error != rpc.InterceptError_UNSPECIFIED {
		return result, nil
	}

	spec.ServiceUid = result.ServiceUid
	spec.WorkloadKind = result.WorkloadKind

	deleteMount := false
	if ir.MountPoint != "" {
		// Ensure that the mount-point is free to use
		if prev, loaded := tm.mountPoints.LoadOrStore(ir.MountPoint, spec.Name); loaded {
			return &rpc.InterceptResult{
				InterceptInfo: nil,
				Error:         rpc.InterceptError_MOUNT_POINT_BUSY,
				ErrorText:     prev.(string),
			}, nil
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

	apiKey, err := tm.getAPIKey(c, "agent-"+spec.Mechanism, false)
	if err != nil {
		dlog.Errorf(c, "error getting apiKey for agent: %s", err)
	}
	dlog.Debugf(c, "creating intercept %s", spec.Name)
	tos := &client.GetConfig(c).Timeouts
	c, cancel := tos.TimeoutContext(c, client.TimeoutIntercept)
	defer cancel()
	<-tm.startup
	ii, err := tm.managerClient.CreateIntercept(c, &manager.CreateInterceptRequest{
		Session:       tm.session(),
		InterceptSpec: spec,
		ApiKey:        apiKey,
	})
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		err = client.CheckTimeout(c, err)
		return &rpc.InterceptResult{Error: rpc.InterceptError_TRAFFIC_MANAGER_ERROR, ErrorText: err.Error()}, nil
	}
	dlog.Debugf(c, "created intercept %s", ii.Spec.Name)

	// The agent is in place and the traffic-manager has acknowledged the creation of the intercept. It
	// should become active within a few seconds.
	if ii, err = tm.waitForActiveIntercept(c, ii.Id); err != nil {
		return &rpc.InterceptResult{
			InterceptInfo: ii,
			Error:         rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText:     err.Error(),
		}, nil
	}
	result.InterceptInfo = ii
	if ir.MountPoint != "" && ii.SftpPort > 0 {
		result.Environment["TELEPRESENCE_ROOT"] = ir.MountPoint
		deleteMount = false // Mount-point is busy until intercept ends
		ii.Spec.MountPoint = ir.MountPoint
	}
	return result, nil
}

// addLocalOnlyIntercept adds a local-only intercept
func (tm *trafficManager) addLocalOnlyIntercept(c context.Context, spec *manager.InterceptSpec) (*rpc.InterceptResult, error) {
	tm.accLock.Lock()
	if tm.localInterceptedNamespaces == nil {
		tm.localInterceptedNamespaces = map[string]struct{}{}
	}
	tm.localIntercepts[spec.Name] = spec.Namespace
	_, found := tm.interceptedNamespaces[spec.Namespace]
	if !found {
		_, found = tm.localInterceptedNamespaces[spec.Namespace]
	}
	tm.localInterceptedNamespaces[spec.Namespace] = struct{}{}
	tm.accLock.Unlock()
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

func (tm *trafficManager) addAgent(c context.Context, namespace, agentName, svcName, svcPortIdentifier, agentImageName string) *rpc.InterceptResult {
	svcUID, kind, err := tm.ensureAgent(c, namespace, agentName, svcName, svcPortIdentifier, agentImageName)
	if err != nil {
		if err == agentNotFound {
			return &rpc.InterceptResult{
				Error:     rpc.InterceptError_NOT_FOUND,
				ErrorText: agentName,
			}
		}
		dlog.Error(c, err)
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}

	dlog.Infof(c, "waiting for agent for %s %q.%s", kind, agentName, namespace)
	agent, err := tm.waitForAgent(c, agentName, namespace)
	if err != nil {
		dlog.Error(c, err)
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}
	dlog.Infof(c, "agent created for %s %q.%s", kind, agentName, namespace)
	return &rpc.InterceptResult{
		Error:        rpc.InterceptError_UNSPECIFIED,
		Environment:  agent.Environment,
		ServiceUid:   svcUID,
		WorkloadKind: kind,
	}
}

func (tm *trafficManager) waitForActiveIntercept(ctx context.Context, id string) (*manager.InterceptInfo, error) {
	dlog.Debugf(ctx, "waiting for intercept id=%q to become active", id)
	waitError := func(err error) error {
		return client.CheckTimeout(ctx,
			fmt.Errorf("waiting for intercept id=%q to become active: %w", id, err))
	}
	<-tm.startup
	stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
	if err != nil {
		return nil, waitError(err)
	}
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			return nil, waitError(err)
		}

		var intercept *manager.InterceptInfo
		for _, straw := range snapshot.Intercepts {
			if straw.Id == id {
				intercept = straw
				break
			}
		}

		switch {
		case intercept == nil:
			dlog.Debugf(ctx, "wait status: intercept id=%q does not yet exist", id)
		case intercept.Disposition == manager.InterceptDispositionType_WAITING:
			dlog.Debugf(ctx, "wait status: intercept id=%q is still WAITING", id)
		default:
			dlog.Debugf(ctx, "wait status: intercept id=%q is no longer WAITING; is now %v", id, intercept.Disposition)
			if intercept.Disposition != manager.InterceptDispositionType_ACTIVE {
				return intercept, errors.Errorf("intercept in error state %v: %v", intercept.Disposition, intercept.Message)
			}
			return intercept, nil
		}
	}
}

func (tm *trafficManager) waitForAgent(ctx context.Context, name string, namespace string) (*manager.AgentInfo, error) {
	ctx, cancel := client.GetConfig(ctx).Timeouts.TimeoutContext(ctx, client.TimeoutAgentInstall) // installing a new agent can take some time
	defer cancel()

	waitError := func(err error) error {
		return client.CheckTimeout(ctx, fmt.Errorf("waiting for agent %q to be present: %w", name, err))
	}
	<-tm.startup
	stream, err := tm.managerClient.WatchAgents(ctx, tm.session())
	if err != nil {
		return nil, waitError(err)
	}
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			return nil, waitError(err)
		}

		var agentList []*manager.AgentInfo
		for _, agent := range snapshot.Agents {
			if agent.Name == name && agent.Namespace == namespace {
				agentList = append(agentList, agent)
			}
		}
		if managerutil.AgentsAreCompatible(agentList) {
			return agentList[0], nil
		}
	}
}

// startForwards starts port forwards and mounts for the given forwardKey. It returns true if either was started (port-forward or a mount), false otherwise
func (tm *trafficManager) startForwards(ctx context.Context, wg *sync.WaitGroup, fk forwardKey, sftpPort int32, extraPorts []int32) bool {
	started := false
	if sftpPort > 0 {
		// There's nothing to mount if the SftpPort is zero
		started = true
		mntCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", fk.PodIP, sftpPort))
		wg.Add(1)
		go tm.workerMountForwardIntercept(mntCtx, mountForward{fk, sftpPort}, wg)
	}
	for _, port := range extraPorts {
		started = true
		pfCtx := dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s:%d", fk.PodIP, port))
		wg.Add(1)
		go tm.workerPortForwardIntercept(pfCtx, portForward{fk, port}, wg)
	}
	return started
}

func (tm *trafficManager) workerPortForwardIntercept(ctx context.Context, pf portForward, wg *sync.WaitGroup) {
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
	f := forwarder.NewForwarder(&addr, pf.PodIP, pf.Port)
	err := f.Serve(ctx)
	if err != nil {
		dlog.Errorf(ctx, "port-forwarder failed with %v", err)
	}
}

func (tm *trafficManager) workerMountForwardIntercept(ctx context.Context, mf mountForward, wg *sync.WaitGroup) {
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

	defer func() {
		if _, err := os.Stat(mountPoint); !os.IsNotExist(err) {
			// Remove if empty
			if err := os.Remove(mountPoint); err != nil {
				dlog.Errorf(ctx, "removal of %q failed: %v", mountPoint, err)
			}
		}
		tm.mountPoints.Delete(mountPoint)
		dlog.Infof(ctx, "Removed file system mount %q", mountPoint)
	}()

	dlog.Infof(ctx, "Mounting file system for intercept %q at %q", mf.Name, mountPoint)

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
			"localhost:" + install.TelAppMountPoint, // what to mount
			mountPoint,                              // where to mount it
		}
		return dpipe.DPipe(ctx, dexec.CommandContext(ctx, "sshfs", sshfsArgs...), conn)
	}, 3*time.Second, 6*time.Second)

	if err != nil {
		dlog.Error(ctx, err)
	}
}

// removeIntercept removes one intercept by name
func (tm *trafficManager) removeIntercept(c context.Context, name string) error {
	if ns, ok := tm.localIntercepts[name]; ok {
		return tm.removeLocalOnlyIntercept(c, name, ns)
	}
	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	<-tm.startup
	_, err := tm.managerClient.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: tm.session(),
		Name:    name,
	})
	return err
}

func (tm *trafficManager) removeLocalOnlyIntercept(c context.Context, name, namespace string) error {
	dlog.Debugf(c, "removing local-only intercept %s", name)
	delete(tm.localIntercepts, name)
	for _, otherNs := range tm.localIntercepts {
		if otherNs == namespace {
			return nil
		}
	}

	// Ensure that namespace is removed from localInterceptedNamespaces if this was the last local intercept
	// for the given namespace.
	tm.accLock.Lock()
	delete(tm.localInterceptedNamespaces, namespace)
	tm.accLock.Unlock()
	tm.updateDaemonNamespaces(c)
	return nil
}

// clearIntercepts removes all intercepts
func (tm *trafficManager) clearIntercepts(c context.Context) error {
	<-tm.startup
	intercepts, _ := actions.ListMyIntercepts(c, tm.managerClient, tm.session().SessionId)
	for _, cept := range intercepts {
		err := tm.removeIntercept(c, cept.Spec.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
