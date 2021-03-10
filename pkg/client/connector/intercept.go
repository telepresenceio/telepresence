package connector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
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
)

func (s *service) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_UNSPECIFIED
	msg := ""
	switch {
	case s.cluster == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case s.trafficMgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	case s.trafficMgr.managerClient == nil:
		if s.trafficMgr.managerErr != nil {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
			msg = s.trafficMgr.managerErr.Error()
		} else {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
		}
	}
	return ie, msg
}

type portForward struct {
	ManagerPort int32
	TargetHost  string
	TargetPort  int32
}

type mountForward struct {
	Name      string
	Namespace string
	PodName   string
	SshPort   int32
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

	livePortForwards := make(map[portForward]context.CancelFunc)

	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
		for err == nil {
			var snapshot *manager.InterceptInfoSnapshot
			snapshot, err = stream.Recv()
			if err != nil {
				break
			}
			snapshotPortForwards := make(map[portForward]struct{})
			namespaces := make(map[string]struct{})
			for _, intercept := range snapshot.Intercepts {
				if intercept.Disposition != manager.InterceptDispositionType_ACTIVE {
					continue
				}
				namespaces[intercept.Spec.Namespace] = struct{}{}
				pf := portForward{
					ManagerPort: intercept.ManagerPort,
					TargetHost:  intercept.Spec.TargetHost,
					TargetPort:  intercept.Spec.TargetPort,
				}
				snapshotPortForwards[pf] = struct{}{}
				if _, isLive := livePortForwards[pf]; !isLive {
					pfCtx, pfCancel := context.WithCancel(ctx)
					pfCtx = dgroup.WithGoroutineName(pfCtx,
						fmt.Sprintf("/%d:%s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort))
					livePortForwards[pf] = pfCancel

					wg.Add(1)
					go tm.workerPortForwardIntercept(pfCtx, pf, &wg)

					// There's nothing to mount if the SshPort is zero
					if intercept.SshPort != 0 {
						mf := mountForward{
							Name:      intercept.Spec.Name,
							Namespace: intercept.Spec.Namespace,
							PodName:   intercept.PodName,
							SshPort:   intercept.SshPort,
						}
						wg.Add(1)
						go tm.workerMountForwardIntercept(pfCtx, mf, &wg)
					}
				}
			}
			for pf, cancel := range livePortForwards {
				if _, isWanted := snapshotPortForwards[pf]; !isWanted {
					dlog.Infof(ctx, "Terminating port-forward manager:%d -> %s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort)
					cancel()
					delete(livePortForwards, pf)
				}
			}
			tm.setInterceptedNamespaces(ctx, namespaces)
		}

		if ctx.Err() == nil {
			dlog.Errorf(ctx, "communicating with manager: %v", err)
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

	if _, inUse := tm.localIntercepts[spec.Name]; inUse {
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_ALREADY_EXISTS,
			ErrorText: spec.Name,
		}, nil
	}

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
	if result = tm.addAgent(c, spec.Namespace, spec.Agent, spec.ServicePortName, ir.AgentImage); result.Error != rpc.InterceptError_UNSPECIFIED {
		return result, nil
	}

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

	dlog.Debugf(c, "creating intercept %s", spec.Name)
	ii, err := tm.managerClient.CreateIntercept(c, &manager.CreateInterceptRequest{
		Session:       tm.session(),
		InterceptSpec: spec,
	})
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		return &rpc.InterceptResult{Error: rpc.InterceptError_TRAFFIC_MANAGER_ERROR, ErrorText: err.Error()}, nil
	}
	dlog.Debugf(c, "created intercept %s", ii.Spec.Name)

	// The agent is in place and the traffic-manager has acknowledged the creation of the intercept. It
	// should become active within a few seconds.
	c, cancel := context.WithTimeout(c, 5*time.Second)
	defer cancel()
	if ii, err = tm.waitForActiveIntercept(c, ii.Id); err != nil {
		return &rpc.InterceptResult{
			InterceptInfo: ii,
			Error:         rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText:     err.Error(),
		}, nil
	}
	result.InterceptInfo = ii
	if ir.MountPoint != "" && ii.SshPort > 0 {
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

func (tm *trafficManager) addAgent(c context.Context, namespace, agentName, svcPort, agentImageName string) *rpc.InterceptResult {
	if err := tm.ensureAgent(c, namespace, agentName, svcPort, agentImageName); err != nil {
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

	dlog.Infof(c, "waiting for agent for deployment %q", agentName)
	agent, err := tm.waitForAgent(c, agentName)
	if err != nil {
		dlog.Error(c, err)
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}
	dlog.Infof(c, "agent created for deployment %q", agentName)
	return &rpc.InterceptResult{
		Error:       rpc.InterceptError_UNSPECIFIED,
		Environment: agent.Environment,
	}
}

func (tm *trafficManager) waitForActiveIntercept(ctx context.Context, id string) (*manager.InterceptInfo, error) {
	dlog.Debugf(ctx, "waiting for intercept id=%q to become active", id)
	stream, err := tm.managerClient.WatchIntercepts(ctx, tm.session())
	for err != nil {
		return nil, fmt.Errorf("waiting for intercept id=%q to become active: %w", id, err)
	}
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("waiting for intercept id=%q to become active: %w", id, err)
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

func (tm *trafficManager) waitForAgent(ctx context.Context, name string) (*manager.AgentInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second) // installing a new agent can take some time
	defer cancel()

	stream, err := tm.managerClient.WatchAgents(ctx, tm.session())
	if err != nil {
		return nil, fmt.Errorf("waiting for agent %q to be present: %q", name, err)
	}
	for {
		snapshot, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("waiting for agent %q to be present: %q", name, err)
		}

		var agentList []*manager.AgentInfo
		for _, agent := range snapshot.Agents {
			if agent.Name == name {
				agentList = append(agentList, agent)
			}
		}
		if managerutil.AgentsAreCompatible(agentList) {
			return agentList[0], nil
		}
	}
}

func (tm *trafficManager) workerPortForwardIntercept(ctx context.Context, pf portForward, wg *sync.WaitGroup) {
	defer wg.Done()

	dlog.Infof(ctx, "Initiating port-forward manager:%d -> %s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort)

	sshArgs := []string{
		"ssh",

		"-F", "none", // don't load the user's config file

		// connection settings
		"-C", // compression
		"-oConnectTimeout=10",
		"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
		"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either

		// port-forward settings
		"-N", // no remote command; just connect and forward ports
		"-oExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("%d:%s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort), // port to forward

		// where to connect to
		"-p", strconv.Itoa(int(tm.sshPort)),
		"telepresence@localhost",
	}

	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		start := time.Now()
		err := dexec.CommandContext(ctx, sshArgs[0], sshArgs[1:]...).Run()
		lifetime := time.Since(start)

		if ctx.Err() == nil {
			if err == nil {
				err = errors.New("process terminated unexpectedly")
			}
			dlog.Errorf(ctx, "communicating with manager: %v", err)
			if lifetime >= 20*time.Second {
				backoff = 100 * time.Millisecond
				dtime.SleepWithContext(ctx, backoff)
			} else {
				dtime.SleepWithContext(ctx, backoff)
				backoff *= 2
				if backoff > 3*time.Second {
					backoff = 3 * time.Second
				}
			}
		}
	}

	dlog.Infof(ctx, "Terminated port-forward manager:%d -> %s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort)
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

	// kubectl port-forward arguments
	pfArgs := []string{
		// Port forward to pod
		"--namespace",
		mf.Namespace,
		mf.PodName,

		// from dynamically allocated local port to the pods sshPort
		fmt.Sprintf(":%d", mf.SshPort),
	}

	rxPortForward := regexp.MustCompile(`\AForwarding from \d+\.\d+\.\d+\.\d+:(\d+) `)

	// kubectl port-forward output scanner that captures the dynamically assigned local port
	outputScanner := func(sc *bufio.Scanner) interface{} {
		for sc.Scan() {
			text := sc.Text()
			dlog.Debug(ctx, text)
			if rgs := rxPortForward.FindStringSubmatch(text); rgs != nil {
				return rgs[1]
			}
		}
		if err := sc.Err(); err != nil {
			dlog.Error(ctx, err)
		}
		return nil
	}

	// Retry mount in case it gets disconnected
	err := client.Retry(ctx, "kubectl port-forward to pod", func(ctx context.Context) error {
		return tm.portForwardAndThen(ctx, pfArgs, outputScanner, func(ctx context.Context, rg interface{}) error {
			localPort := rg.(string)
			sshArgs := []string{
				"-F", "none", // don't load the user's config file
				"-f", // foreground operation

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",
				"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
				"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either

				"-p", localPort, // port to connect to

				// mount directives
				"telepresence@localhost:" + telAppMountPoint, // remote user and what to mount
				mountPoint, // where to mount it
			}

			err := dexec.CommandContext(ctx, "sshfs", sshArgs...).Run()
			if err != nil && ctx.Err() != nil {
				err = nil
			}
			return err
		})
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
	intercepts, _ := actions.ListMyIntercepts(c, tm.managerClient, tm.session().SessionId)
	for _, cept := range intercepts {
		err := tm.removeIntercept(c, cept.Spec.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
