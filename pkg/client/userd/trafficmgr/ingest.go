package trafficmgr

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
)

type ingestKey struct {
	workload  string
	container string
}

func (ik ingestKey) String() string {
	return fmt.Sprintf("%s[%s]", ik.workload, ik.container)
}

type ingest struct {
	*manager.AgentInfo
	ingestKey
	wg               sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
	localMountPoint  string
	localMountPort   int32
	localPorts       []string
	handlerContainer string
	pid              int
	mounter          remotefs.Mounter
}

func (ig *ingest) podAccess(rd daemon.DaemonClient) *podAccess {
	ni := ig.Containers[ig.container]
	pa := &podAccess{
		ctx:              ig.ctx,
		localPorts:       ig.localPorts,
		workload:         ig.workload,
		container:        ig.container,
		podIP:            ig.PodIp,
		sftpPort:         ig.SftpPort,
		ftpPort:          ig.FtpPort,
		mountPoint:       ni.MountPoint,
		clientMountPoint: ig.localMountPoint,
		localMountPort:   ig.localMountPort,
		mounter:          &ig.mounter,
		readOnly:         true,
		wg:               &ig.wg,
	}
	if err := pa.ensureAccess(ig.ctx, rd); err != nil {
		clog.Error(ig.ctx, err)
	}
	return pa
}

func (ig *ingest) response() *rpc.IngestInfo {
	cn := ig.Containers[ig.container]
	ii := &rpc.IngestInfo{
		Workload:         ig.workload,
		WorkloadKind:     ig.Kind,
		Container:        ig.container,
		PodIp:            ig.PodIp,
		SftpPort:         ig.SftpPort,
		FtpPort:          ig.FtpPort,
		MountPoint:       cn.MountPoint,
		Mounts:           cn.Mounts,
		ClientMountPoint: ig.localMountPoint,
		Environment:      cn.Environment,
	}
	if ig.handlerContainer != "" {
		if ii.Environment == nil {
			ii.Environment = make(map[string]string, 1)
		}
		ii.Environment["TELEPRESENCE_HANDLER_CONTAINER_NAME"] = ig.handlerContainer
	}
	return ii
}

func (s *session) getSingleContainerName(ai *manager.AgentInfo) (name string, err error) {
	if err = s.validateAgentForIngest(ai); err != nil {
		return "", err
	}
	if len(ai.Containers) > 1 {
		return "", status.Error(codes.NotFound, fmt.Sprintf("workload %s has multiple containers. Please specify which one to use", ai.Name))
	}
	for name = range ai.Containers {
	}
	return name, err
}

func (s *session) validateAgentForIngest(ai *manager.AgentInfo) error {
	if len(ai.Containers) == 0 {
		return status.Error(codes.Unimplemented, fmt.Sprintf("traffic-manager %s has no support for ingest", s.managerVersion))
	}
	return nil
}

func (s *session) getCurrentAgent(name string) *manager.AgentInfo {
	for _, ai := range s.getCurrentAgents() {
		if ai.Name == name {
			return ai
		}
	}
	return nil
}

func (s *session) Ingest(ctx context.Context, rq *rpc.IngestRequest) (ir *rpc.IngestInfo, err error) {
	id := rq.Identifier
	ik := ingestKey{
		workload:  id.WorkloadName,
		container: id.ContainerName,
	}
	ai := s.getCurrentAgent(ik.workload)

	if ai != nil {
		if ik.container == "" {
			ik.container, err = s.getSingleContainerName(ai)
			if err != nil {
				return nil, err
			}
		}
		if ig, loaded := s.currentIngests.Load(ik); loaded {
			return ig.response(), nil
		}
	}

	err = s.ensureNoMountConflict(rq.MountPoint, rq.LocalMountPort)
	if err != nil {
		return nil, err
	}

	if ai == nil {
		var as *manager.AgentInfoSnapshot
		timeoutCtx, cancel := client.GetConfig(s).Timeouts().TimeoutContext(s, client.TimeoutTrafficAgentArrival)
		defer cancel()
		as, err = s.ManagerClient().EnsureAgent(timeoutCtx, &manager.EnsureAgentRequest{Session: s.sessionInfo, Name: ik.workload})
		if err != nil {
			return nil, err
		}
		ai = as.Agents[0]
	}
	if err = s.validateAgentForIngest(ai); err != nil {
		return nil, err
	}

	if ik.container == "" {
		ik.container, err = s.getSingleContainerName(ai)
		if err != nil {
			return nil, err
		}
	} else if _, ok := ai.Containers[ik.container]; !ok {
		return nil, fmt.Errorf("workload %s has no container named %s", ik.workload, ik.container)
	}

	err = s.translateContainerEnv(ctx, ai, ik.container)
	if err != nil {
		return nil, err
	}

	ig, loaded := s.currentIngests.LoadOrCompute(ik, func() (*ingest, bool) {
		ctx, cancel := context.WithCancel(s)
		cancelIngest := func() {
			s.currentIngests.Delete(ik)
			clog.Debugf(ctx, "Cancelling ingest %s", ik)
			cancel()
			s.ingestTracker.cancelContainer(ik.workload, ik.container)
		}
		return &ingest{
			ingestKey:       ik,
			AgentInfo:       ai,
			ctx:             ctx,
			cancel:          cancelIngest,
			localMountPoint: rq.MountPoint,
			localMountPort:  rq.LocalMountPort,
			localPorts:      rq.LocalPorts,
		}, false
	})
	if !loaded {
		s.ingestTracker.initialStart(ig.podAccess(s.rootDaemon))
	}
	return ig.response(), nil
}

func (s *session) translateContainerEnv(ctx context.Context, ai *manager.AgentInfo, container string) error {
	cn, ok := ai.Containers[container]
	if !ok {
		return fmt.Errorf("workload %s has no container named %s", ai.Name, container)
	}
	return s.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
		env, err := s.rootDaemon.TranslateEnvIPs(s, &daemon.Environment{Env: cn.Environment})
		if err == nil {
			cn.Environment = env.Env
		}
		return err
	})
}

func (s *session) getCurrentIngests() []*rpc.IngestInfo {
	ingests := make([]*rpc.IngestInfo, 0, s.currentIngests.Size())
	s.currentIngests.Range(func(key ingestKey, ig *ingest) bool {
		ingests = append(ingests, ig.response())
		return true
	})
	return ingests
}

func (s *session) getIngest(rq *rpc.IngestIdentifier) (ig *ingest, err error) {
	if rq.ContainerName == "" {
		// Valid if there's only one ingest for the given workload.
		s.currentIngests.Range(func(key ingestKey, value *ingest) bool {
			if key.workload == rq.WorkloadName {
				if rq.ContainerName != "" {
					err = status.Error(codes.NotFound, fmt.Sprintf("workload %s has multiple ingests. Please specify which one to use", rq.WorkloadName))
					return false
				}
				rq.ContainerName = key.container
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if rq.ContainerName == "" {
			return nil, status.Error(codes.NotFound, fmt.Sprintf("no ingest found for workload %s", rq.WorkloadName))
		}
	}
	ik := ingestKey{
		workload:  rq.WorkloadName,
		container: rq.ContainerName,
	}
	if ig, ok := s.currentIngests.Load(ik); ok {
		return ig, nil
	}
	return nil, status.Error(codes.NotFound, fmt.Sprintf("ingest %s doesn't exist", ik))
}

func (s *session) GetIngest(rq *rpc.IngestIdentifier) (ii *rpc.IngestInfo, err error) {
	ig, err := s.getIngest(rq)
	if err != nil {
		return nil, err
	}
	return ig.response(), nil
}

func (s *session) LeaveIngest(rq *rpc.IngestIdentifier) (ii *rpc.IngestInfo, err error) {
	ig, err := s.getIngest(rq)
	if err != nil {
		return nil, err
	}
	s.stopHandler(fmt.Sprintf("%s/%s", ig.workload, ig.container), ig.handlerContainer, ig.pid)
	ig.cancel()
	ig.wg.Wait()
	return ig.response(), nil
}
