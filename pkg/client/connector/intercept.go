package connector

import (
	"context"
	"encoding/json"
	"fmt"
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
			for _, intercept := range snapshot.Intercepts {
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
					go tm.workerPortForwardIntercept(pfCtx, pf)
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
func (tm *trafficManager) addIntercept(c, longLived context.Context, ir *manager.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	spec := ir.InterceptSpec
	intercepts, err := actions.ListMyIntercepts(c, tm.managerClient, tm.session().SessionId)
	if err != nil {
		return nil, err
	}
	for _, iCept := range intercepts {
		if iCept.Spec.Name == spec.Name {
			return &rpc.InterceptResult{
				Error:     rpc.InterceptError_ALREADY_EXISTS,
				ErrorText: iCept.Spec.Name,
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

	if found == nil {
		if result := tm.addAgent(c, agentName, agentImageName(c, tm.env)); result != nil {
			return result, nil
		}
	} else {
		dlog.Infof(c, "found agent for deployment %q", agentName)
	}

	ir.Session = tm.session()
	dlog.Debugf(c, "creating intercept %s", ir.InterceptSpec.Name)
	ii, err := tm.managerClient.CreateIntercept(c, ir)

	result := &rpc.InterceptResult{InterceptInfo: ii}
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		result.Error = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
		result.ErrorText = err.Error()
		return result, nil
	}
	dlog.Debugf(c, "created intercept %s", ii.Spec.Name)
	ii, err = tm.waitForActiveIntercept(c, ii.Id)
	if err != nil {
		_ = tm.removeIntercept(c, spec.Name)
		result.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
		result.ErrorText = err.Error()
		return result, nil
	}
	result.InterceptInfo = ii

	return result, nil
}

func (tm *trafficManager) addAgent(c context.Context, agentName, agentImageName string) *rpc.InterceptResult {
	if err := tm.installer.ensureAgent(c, agentName, "", agentImageName); err != nil {
		if err == agentExists {
			return nil
		}
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

	dlog.Infof(c, "waiting for new agent for deployment %q", agentName)
	_, err := tm.waitForAgent(c, agentName)
	if err != nil {
		dlog.Error(c, err)
		return &rpc.InterceptResult{
			Error:     rpc.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}
	dlog.Infof(c, "agent created for deployment %q", agentName)
	return nil
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

		for _, agent := range snapshot.Agents {
			if agent.Name == name {
				return agent, nil
			}
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
