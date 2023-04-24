package trafficmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// getCurrentAgents returns a copy of the current agent snapshot
// Deprecated.
func (s *session) getCurrentAgents() []*manager.AgentInfo {
	// Copy the current snapshot
	s.currentAgentsLock.Lock()
	agents := make([]*manager.AgentInfo, len(s.currentAgents))
	for i, ii := range s.currentAgents {
		agents[i] = proto.Clone(ii).(*manager.AgentInfo)
	}
	s.currentAgentsLock.Unlock()
	return agents
}

// getCurrentAgentsInNamespace returns a map of agents matching the given namespace from the current agent snapshot.
// The map contains the first agent for each name found. Agents from replicas of the same workload are ignored.
// Deprecated.
func (s *session) getCurrentAgentsInNamespace(ns string) map[string]*manager.AgentInfo {
	// Copy the current snapshot
	s.currentAgentsLock.Lock()
	agents := make(map[string]*manager.AgentInfo)
	for _, ii := range s.currentAgents {
		if ii.Namespace == ns {
			// There may be any number or replicas of the agent. Avoid cloning all of them.
			if _, ok := agents[ii.Name]; !ok {
				agents[ii.Name] = proto.Clone(ii).(*manager.AgentInfo)
			}
		}
	}
	s.currentAgentsLock.Unlock()
	return agents
}

func (s *session) getCurrentSidecarsInNamespace(ctx context.Context, ns string) map[string]*agentconfig.Sidecar {
	sidecars := make(map[string]*agentconfig.Sidecar)

	// Load configmap entry from the telepresence-agents configmap
	cm, err := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(ns).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !k8sErrors.IsNotFound(err) {
			dlog.Error(ctx, errcat.User.New(err))
		}
		return sidecars
	}

	for workload, sidecar := range cm.Data {
		var cfg agentconfig.Sidecar
		if err = yaml.Unmarshal([]byte(sidecar), &cfg); err != nil {
			dlog.Errorf(ctx, "Unable to parse entry for %q in configmap %q: %v", workload, agentconfig.ConfigMap, err)
			return sidecars
		}
		sidecars[workload] = &cfg
	}

	return sidecars
}

type agentsStringer []*manager.AgentInfo

func (as agentsStringer) String() string {
	sb := bytes.Buffer{}
	sb.WriteByte('[')
	for i, a := range as {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(a.Name)
		sb.WriteByte('.')
		sb.WriteString(a.Namespace)
	}
	sb.WriteByte(']')
	return sb.String()
}

func (s *session) setCurrentAgents(ctx context.Context, agents []*manager.AgentInfo) {
	s.currentAgentsLock.Lock()
	s.currentAgents = agents
	dlog.Debugf(ctx, "setCurrentAgents %s", agentsStringer(agents))
	s.currentAgentsLock.Unlock()
}

func (s *session) notifyAgentWatchers(ctx context.Context, agents []*manager.AgentInfo) {
	s.currentAgentsLock.Lock()
	aiws := s.agentInitWaiters
	s.agentInitWaiters = nil
	s.currentAgentsLock.Unlock()
	for _, aiw := range aiws {
		close(aiw)
	}

	// Notify waiters for agents
	for _, agent := range agents {
		fullName := agent.Name + "." + agent.Namespace
		if chUt, loaded := s.agentWaiters.LoadAndDelete(fullName); loaded {
			if ch, ok := chUt.(chan *manager.AgentInfo); ok {
				dlog.Debugf(ctx, "wait status: agent %s arrived", fullName)
				ch <- agent
				close(ch)
			}
		}
	}
}

func (s *session) watchAgentsNS(ctx context.Context) error {
	// Cancel this watcher whenever the set of active namespaces change
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.addActiveNamespaceListener(func() {
		cancel()
	})

	nss := s.getActiveNamespaces(ctx)
	if len(nss) == 0 {
		// Not much point in watching for nothing, so just wait until
		// the set of namespaces change. Returning nil here means that
		// we want a restart unless the caller too is cancelled
		<-ctx.Done()
		return nil
	}

	dlog.Debugf(ctx, "start watchAgentNS %v", nss)
	defer dlog.Debugf(ctx, "end watchAgentNS %v", nss)

	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
	}

	wm := "WatchAgentsNS"
	stream, err := s.managerClient.WatchAgentsNS(ctx, &manager.AgentsRequest{
		Session:    s.SessionInfo(),
		Namespaces: nss,
	}, opts...)
	if err != nil {
		if ctx.Err() == nil {
			err = fmt.Errorf("manager.WatchAgentsNS dial: %w", err)
		} else {
			err = nil
		}
		s.setCurrentAgents(ctx, nil)
		return err
	}

	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			if gErr, ok := status.FromError(err); ok && gErr.Code() == codes.Unimplemented {
				// Fall back to old method of watching all namespaces
				wm = "WatchAgents"
				err = s.watchAgents(ctx, opts)
			}
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				dlog.Errorf(ctx, "manager.%s recv: %v", wm, err)
			} else {
				err = nil
			}
			s.setCurrentAgents(ctx, nil)
			return err
		}
		s.setCurrentAgents(ctx, snapshot.Agents)
		s.notifyAgentWatchers(ctx, snapshot.Agents)
	}
	return nil
}

func (s *session) watchAgents(ctx context.Context, opts []grpc.CallOption) error {
	stream, err := s.managerClient.WatchAgents(ctx, s.SessionInfo(), opts...)
	if err != nil {
		return err
	}

	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			return err
		}
		s.setCurrentAgents(ctx, snapshot.Agents)
		s.notifyAgentWatchers(ctx, snapshot.Agents)
	}
	return nil
}

func (s *session) agentInfoWatcher(ctx context.Context) error {
	return runWithRetry(ctx, s.watchAgentsNS)
}

// Deprecated.
func (s *session) addAgent(
	c context.Context,
	svcProps *interceptInfo,
	agentImageName string,
	telepresenceAPIPort uint16,
) (map[string]string, *rpc.InterceptResult) {
	workload := svcProps.workload
	agentName := workload.GetName()
	namespace := workload.GetNamespace()
	_, kind, err := legacyEnsureAgent(c, s.Cluster, workload, svcProps, agentImageName, telepresenceAPIPort)
	if err != nil {
		if err == errAgentNotFound {
			return nil, &rpc.InterceptResult{
				Error:     common.InterceptError_NOT_FOUND,
				ErrorText: agentName,
			}
		}
		dlog.Error(c, err)
		return nil, &rpc.InterceptResult{
			Error:     common.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}

	dlog.Infof(c, "Waiting for agent for %s %s.%s", kind, agentName, namespace)
	ai, err := s.waitForAgent(c, agentName, namespace)
	if err != nil {
		dlog.Error(c, err)
		return nil, &rpc.InterceptResult{
			Error:     common.InterceptError_FAILED_TO_ESTABLISH,
			ErrorText: err.Error(),
		}
	}
	dlog.Infof(c, "Agent found or created for %s %s.%s", kind, agentName, namespace)
	return ai.Environment, svcProps.InterceptResult()
}

// Deprecated.
func (s *session) waitForAgent(ctx context.Context, name, namespace string) (*manager.AgentInfo, error) {
	fullName := name + "." + namespace
	waitCh := make(chan *manager.AgentInfo)
	s.agentWaiters.Store(fullName, waitCh)
	s.wlWatcher.ensureStarted(ctx, namespace, nil)
	defer s.agentWaiters.Delete(fullName)

	// Agent may already exist.
	for _, agent := range s.getCurrentAgentsInNamespace(namespace) {
		if agent.Name == name {
			return agent, nil
		}
	}

	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutAgentInstall) // installing a new agent can take some time
	defer cancel()

	select {
	case <-ctx.Done():
		return nil, client.CheckTimeout(ctx, fmt.Errorf("waiting for agent %q to be present", fullName))
	case agent := <-waitCh:
		return agent, nil
	}
}
