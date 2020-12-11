package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

func (s *service) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_UNSPECIFIED
	msg := ""
	switch {
	case s.cluster == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case s.trafficMgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	case s.trafficMgr.grpc == nil:
		if s.trafficMgr.apiErr != nil {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
			msg = s.trafficMgr.apiErr.Error()
		} else {
			ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
		}
	}
	return ie, msg
}

// addIntercept adds one intercept
func (tm *trafficManager) addIntercept(c, longLived context.Context, ir *manager.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	spec := ir.InterceptSpec
	spec.Client = tm.userAndHost
	if spec.Mechanism == "" {
		spec.Mechanism = "tcp"
	}

	agentName := spec.Agent
	if spec.Name == "" {
		spec.Name = agentName
	}

	ags := tm.agentInfoSnapshot()
	var found []*manager.AgentInfo
	if ags != nil {
		for _, ag := range ags.Agents {
			if ag.Name != spec.Agent {
				continue
			}
			for _, m := range ag.Mechanisms {
				if spec.Mechanism == m.Name {
					found = append(found, ag)
					break
				}
			}
		}
	}

	result := &rpc.InterceptResult{}
	switch len(found) {
	case 0:
		if err := tm.installer.ensureAgent(c, agentName, ""); err != nil {
			if err == agentExists {
				// the agent exists although it has not been reported yet
				break
			}
			dlog.Error(c, err)
			if err == agentNotFound {
				result.Error = rpc.InterceptError_NOT_FOUND
				result.ErrorText = agentName
			} else {
				result.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
				result.ErrorText = err.Error()
			}
			return result, nil
		}
		dlog.Infof(c, "waiting for new agent for deployment %q", agentName)
		_, err := tm.waitForAgent(c, agentName)
		if err != nil {
			dlog.Error(c, err)
			result.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
			result.ErrorText = err.Error()
			return result, nil
		}
		dlog.Infof(c, "agent created for deployment %q", agentName)
	case 1:
		dlog.Infof(c, "found agent for deployment %q", agentName)
	default:
		txt, _ := json.Marshal(found)
		result.ErrorText = string(txt)
		result.Error = rpc.InterceptError_AMBIGUOUS_MATCH
		return result, nil
	}

	ir.Session = tm.session()
	js, _ := json.Marshal(ir)
	dlog.Debugf(c, "CreateIntercept request: %s", string(js))
	ii, err := tm.grpc.CreateIntercept(c, ir)
	if err != nil {
		dlog.Debugf(c, "manager responded to CreateIntercept with error %v", err)
		result.Error = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
		result.ErrorText = err.Error()
		return result, nil
	}
	js, _ = json.Marshal(ii)
	dlog.Debugf(c, "CreateIntercept response: %s", string(js))
	ii, err = tm.waitForActiveIntercept(c, ii.Id)
	if err != nil {
		_ = tm.removeIntercept(c, spec.Name)
		result.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
		result.ErrorText = err.Error()
		return result, nil
	}

	err = tm.makeIntercept(c, longLived, ii)
	if err != nil {
		_ = tm.removeIntercept(c, spec.Name)
		result.Error = rpc.InterceptError_FAILED_TO_ESTABLISH
		result.ErrorText = err.Error()
		return result, nil
	}

	result.InterceptInfo = ii
	return result, nil
}

func (tm *trafficManager) waitForActiveIntercept(c context.Context, id string) (*manager.InterceptInfo, error) {
	done := make(chan *manager.InterceptInfo)

	il := &iiActive{id: id, done: done}
	go func() {
		if cis := tm.iiListener.getData(); cis != nil {
			// Send initial snapshot to listener
			il.onData(cis)
		}
		tm.iiListener.addListener(il)
	}()
	defer tm.iiListener.removeListener(il)

	dlog.Debugf(c, "waiting for intercept with id %s to become active", id)
	c, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	select {
	case ii := <-done:
		if ii.Disposition == manager.InterceptDispositionType_ACTIVE {
			return ii, nil
		}
		dlog.Errorf(c, "intercept id: %s, state: %s, message: %s", id, ii.Disposition, ii.Message)
		return nil, errors.New(ii.Message)
	case <-c.Done():
		return nil, fmt.Errorf("%v while waiting for intercept with id %s to become active", c.Err(), id)
	}
}

func (tm *trafficManager) waitForAgent(c context.Context, name string) (*manager.AgentInfo, error) {
	done := make(chan *manager.AgentInfo)

	al := &aiPresent{name: name, done: done}
	go func() {
		if cas := tm.aiListener.getData(); cas != nil {
			// Send initial snapshot to listener
			al.onData(cas)
		}
		tm.aiListener.addListener(al)
	}()
	defer tm.aiListener.removeListener(al)

	c, cancel := context.WithTimeout(c, 120*time.Second) // installing a new agent can take some time
	defer cancel()
	select {
	case ai := <-done:
		return ai, nil
	case <-c.Done():
		return nil, fmt.Errorf("%v while waiting for agent %s to be present", c.Err(), name)
	}
}

// makeIntercept acquires an intercept and returns a Resource handle
// for it
func (tm *trafficManager) makeIntercept(c, longLived context.Context, ii *manager.InterceptInfo) error {
	is := ii.Spec
	dlog.Infof(c, "%s: Intercepting via port %v", is.Name, ii.ManagerPort)

	sshArgs := []string{
		"-C", "-N", "telepresence@localhost",
		"-oConnectTimeout=10", "-oExitOnForwardFailure=yes",
		"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null",
		"-p", strconv.Itoa(int(tm.sshPort)),
		"-R", fmt.Sprintf("%d:%s:%d", ii.ManagerPort, is.TargetHost, is.TargetPort),
	}

	dlog.Infof(c, "%s: starting SSH tunnel", is.Name)
	tm.myIntercept = is.Name
	c, tm.cancelIntercept = context.WithCancel(longLived)
	c = dgroup.WithGoroutineName(c, ii.Id)
	return client.Retry(c, "ssh reverse tunnelling", func(c context.Context) error {
		return dexec.CommandContext(c, "ssh", sshArgs...).Start()
	})
}

// removeIntercept removes one intercept by name
func (tm *trafficManager) removeIntercept(c context.Context, name string) error {
	if name == tm.myIntercept {
		dlog.Debugf(c, "cancelling intercept %s", name)
		tm.myIntercept = ""
		tm.cancelIntercept()
	}
	dlog.Debugf(c, "telling manager to remove intercept %s", name)
	_, err := tm.grpc.RemoveIntercept(c, &manager.RemoveInterceptRequest2{
		Session: tm.session(),
		Name:    name,
	})
	return err
}

// clearIntercepts removes all intercepts
func (tm *trafficManager) clearIntercepts(c context.Context) error {
	is := tm.interceptInfoSnapshot()
	if is == nil {
		return nil
	}
	for _, cept := range is.Intercepts {
		err := tm.removeIntercept(c, cept.Spec.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

// An iiListener keeps track of the latest received InterceptInfoSnapshot and provides the
// watcher needed to register other listeners.
type iiListener struct {
	watcher
	data atomic.Value
}

func (il *iiListener) getData() *manager.InterceptInfoSnapshot {
	v := il.data.Load()
	if v == nil {
		return nil
	}
	return v.(*manager.InterceptInfoSnapshot)
}
