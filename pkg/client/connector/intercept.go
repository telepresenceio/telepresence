package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/datawire/telepresence2/rpc/v2/connector"
	"github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/actions"
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
	Name    string
	PodName string
	SshPort int32
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
			bts, _ := json.MarshalIndent(snapshot.Intercepts, "", "  ")
			dlog.Info(ctx, string(bts))
			snapshotPortForwards := make(map[portForward]struct{})
			for _, intercept := range snapshot.Intercepts {
				if intercept.Disposition != manager.InterceptDispositionType_ACTIVE {
					continue
				}
				pf := portForward{
					ManagerPort: intercept.ManagerPort,
					TargetHost:  intercept.Spec.TargetHost,
					TargetPort:  intercept.Spec.TargetPort,
				}
				mf := mountForward{
					Name:    intercept.Spec.Name,
					PodName: intercept.PodName,
					SshPort: intercept.SshPort,
				}
				snapshotPortForwards[pf] = struct{}{}
				if _, isLive := livePortForwards[pf]; !isLive {
					pfCtx, pfCancel := context.WithCancel(ctx)
					pfCtx = dgroup.WithGoroutineName(pfCtx,
						fmt.Sprintf("/%d:%s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort))
					livePortForwards[pf] = pfCancel

					wg.Add(2)
					go tm.workerPortForwardIntercept(pfCtx, pf)
					go tm.workerMountForwardIntercept(pfCtx, mf)
				}
			}
			for pf, cancel := range livePortForwards {
				if _, isWanted := snapshotPortForwards[pf]; !isWanted {
					dlog.Infof(ctx, "Terminating port-forward manager:%d -> %s:%d", pf.ManagerPort, pf.TargetHost, pf.TargetPort)
					cancel()
					delete(livePortForwards, pf)
				}
			}
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
	spec := &manager.InterceptSpec{
		Name:       ir.Name,
		Agent:      ir.AgentName,
		Mechanism:  ir.MatchMechanism,
		Additional: ir.MatchAdditional,
		TargetHost: ir.TargetHost,
		TargetPort: ir.TargetPort,
	}
	intercepts, err := actions.ListMyIntercepts(c, tm.managerClient, tm.session().SessionId)
	if err != nil {
		return nil, err
	}
	for _, iCept := range intercepts {
		if iCept.Spec.Name == ir.Name {
			return &rpc.InterceptResult{
				Error:     rpc.InterceptError_ALREADY_EXISTS,
				ErrorText: iCept.Spec.Name,
			}, nil
		}
		if iCept.Spec.TargetPort == ir.TargetPort && iCept.Spec.TargetHost == ir.TargetHost {
			return &rpc.InterceptResult{
				InterceptInfo: &manager.InterceptInfo{Spec: spec},
				Error:         rpc.InterceptError_LOCAL_TARGET_IN_USE,
				ErrorText:     iCept.Spec.Name,
			}, nil
		}
	}

	spec.Client = tm.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	agentName := spec.Agent
	if spec.Name == "" {
		spec.Name = agentName
	}

	hasSpecMechanism := func(a *manager.AgentInfo) bool {
		for _, mech := range a.Mechanisms {
			if spec.Mechanism == mech.Name {
				return true
			}
		}
		return false
	}

	var found *manager.AgentInfo
	if ags, _ := actions.ListAllAgents(c, tm.managerClient, tm.session().SessionId); ags != nil {
		for _, ag := range ags {
			if !(ag.Name == spec.Agent && hasSpecMechanism(ag)) {
				continue
			}
			if found == nil {
				found = ag
				continue
			}
			if ag.Version == found.Version && ag.Product == found.Product {
				// Just hostname that differs, this is a replica
				continue
			}
			txt, _ := json.Marshal([]*manager.AgentInfo{ag, found})
			return &rpc.InterceptResult{
				InterceptInfo: nil,
				Error:         rpc.InterceptError_AMBIGUOUS_MATCH,
				ErrorText:     string(txt),
			}, nil
		}
	}

	var result *rpc.InterceptResult
	if found == nil {
		if result = tm.addAgent(c, agentName, agentImageName(c, tm.env)); result.Error != rpc.InterceptError_UNSPECIFIED {
			return result, nil
		}
	} else {
		result = &rpc.InterceptResult{
			Environment: found.Environment,
		}
	}

	if ir.MountPoint != "" {
		// Don't overwrite a mount-point for the agent. If a previous mount-point exists, it will
		// be used.
		if prev, loaded := tm.mountPoints.LoadOrStore(ir.MountPoint, ir.Name); loaded {
			return &rpc.InterceptResult{
				InterceptInfo: nil,
				Error:         rpc.InterceptError_MOUNT_POINT_BUSY,
				ErrorText:     prev.(string),
			}, nil
		}
	}

	dlog.Debugf(c, "creating intercept %s", ir.Name)
	ii, err := tm.managerClient.CreateIntercept(c, &manager.CreateInterceptRequest{
		Session:       tm.session(),
		InterceptSpec: spec,
	})
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		return &rpc.InterceptResult{Error: rpc.InterceptError_TRAFFIC_MANAGER_ERROR, ErrorText: err.Error()}, nil
	}
	dlog.Debugf(c, "created intercept %s", ii.Spec.Name)

	ii, err = tm.waitForActiveIntercept(c, ii.Id)
	if err != nil {
		_ = tm.removeIntercept(c, spec.Name)
		return &rpc.InterceptResult{Error: rpc.InterceptError_FAILED_TO_ESTABLISH, ErrorText: err.Error()}, nil
	}
	result.InterceptInfo = ii
	if ir.MountPoint != "" {
		result.Environment["TELEPRESENCE_ROOT"] = ir.MountPoint
	}

	return result, nil
}

func (tm *trafficManager) addAgent(c context.Context, agentName, agentImageName string) *rpc.InterceptResult {
	if err := tm.installer.ensureAgent(c, agentName, "", agentImageName); err != nil {
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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
				return nil, errors.Errorf("intercept in error state %v: %v", intercept.Disposition, intercept.Message)
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

func (tm *trafficManager) workerPortForwardIntercept(ctx context.Context, pf portForward) {
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

func (tm *trafficManager) workerMountForwardIntercept(ctx context.Context, mf mountForward) {
	var mountPoint string
	tm.mountPoints.Range(func(key, value interface{}) bool {
		if mf.Name == value.(string) {
			mountPoint = key.(string)
			return false
		}
		return true
	})
	if mountPoint == "" {
		return
	}
	defer tm.mountPoints.Delete(mountPoint)

	dlog.Infof(ctx, "Mounting file system for intercept %q at %q", mf.Name, mountPoint)

	// kubectl port-forward arguments
	pfArgs := []string{
		// Port forward to pod
		mf.PodName,

		// from dynamically allocated local port to the pods sshPort
		fmt.Sprintf(":%d", mf.SshPort),
	}

	rxPortForward := regexp.MustCompile(`\AForwarding from \d+\.\d+\.\d+\.\d+:(\d+) `)

	// kubectl port-forward output scanner that captures the dynamically assigned local port
	outputScanner := func(sc *bufio.Scanner) interface{} {
		for sc.Scan() {
			if rgs := rxPortForward.FindStringSubmatch(sc.Text()); rgs != nil {
				return rgs[1]
			}
		}
		return nil
	}

	// Retry mount in case it gets disconnected
	err := client.Retry(ctx, "kubectl port-forward to pod", func(ctx context.Context) error {
		return tm.k8sClient.portForwardAndThen(ctx, pfArgs, outputScanner, "sshfs mount", func(ctx context.Context, rg interface{}) error {
			localPort := rg.(string)
			sshArgs := []string{
				"-F", "none", // don't load the user's config file

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",
				"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
				"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either

				"-p", localPort, // port to connect to

				// mount directives
				"telepresence@localhost:/", // remote user and what to mount
				mountPoint,                 // where to mount it
			}

			// Attempt a umount regardless. If context is cancelled, the sshfs command might have succeeded with
			// the mount but still return an error.
			defer func() {
				_ = dexec.CommandContext(context.Background(), "umount", mountPoint).Run()
			}()

			err := dexec.CommandContext(ctx, "sshfs", sshArgs...).Run()
			if err != nil {
				if ctx.Err() != nil {
					err = nil
				}
				return err
			}
			<-ctx.Done()
			return nil
		})
	}, 3*time.Second, 6*time.Second)

	if err != nil {
		dlog.Error(ctx, err)
	}
	dlog.Infof(ctx, "Removed file system mount for intercept")
}

// removeIntercept removes one intercept by name
func (tm *trafficManager) removeIntercept(c context.Context, name string) error {
	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	_, err := tm.managerClient.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: tm.session(),
		Name:    name,
	})
	return err
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
