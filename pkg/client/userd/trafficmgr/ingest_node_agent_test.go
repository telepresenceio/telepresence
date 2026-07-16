package trafficmgr

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// fakeIngestRootDaemon is a minimal daemon.Daemon gRPC server that only echoes
// TranslateEnvIPs, used to let Ingest's translateContainerEnv step succeed
// without a real root daemon.
type fakeIngestRootDaemon struct {
	rootdRpc.UnimplementedDaemonServer
}

func (f *fakeIngestRootDaemon) TranslateEnvIPs(_ context.Context, e *rootdRpc.Environment) (*rootdRpc.Environment, error) {
	return e, nil
}

// withFakeRootDaemon wires s up with a bufconn-backed root daemon that only
// supports TranslateEnvIPs, so Ingest's post-EnsureAgent env-translation step
// succeeds.
func withFakeRootDaemon(t *testing.T, s *session) {
	t.Helper()
	conn, cleanup, err := dialTestRootDaemon(&fakeIngestRootDaemon{})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	s.setRootDaemon(rootdRpc.NewDaemonClient(conn), conn, nil, false)
}

// fakeManagerServer is a minimal manager.ManagerServer used to observe and canned-answer
// the EnsureAgent/ReleaseAgent calls made by session.Ingest and session.LeaveIngest.
type fakeManagerServer struct {
	manager.UnimplementedManagerServer

	ensureAgentResponse *manager.AgentInfoSnapshot
	ensureAgentErr      error

	mu                sync.Mutex
	releaseAgentCalls []*manager.ReleaseAgentRequest
	ensureAgentCalls_ []*manager.EnsureAgentRequest
}

func (f *fakeManagerServer) EnsureAgent(_ context.Context, rq *manager.EnsureAgentRequest) (*manager.AgentInfoSnapshot, error) {
	f.mu.Lock()
	f.ensureAgentCalls_ = append(f.ensureAgentCalls_, rq)
	f.mu.Unlock()
	if f.ensureAgentErr != nil {
		return nil, f.ensureAgentErr
	}
	return f.ensureAgentResponse, nil
}

func (f *fakeManagerServer) ensureAgentCalls() []*manager.EnsureAgentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*manager.EnsureAgentRequest(nil), f.ensureAgentCalls_...)
}

func (f *fakeManagerServer) ReleaseAgent(_ context.Context, rq *manager.ReleaseAgentRequest) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.releaseAgentCalls = append(f.releaseAgentCalls, rq)
	f.mu.Unlock()
	return &emptypb.Empty{}, nil
}

func (f *fakeManagerServer) releaseCalls() []*manager.ReleaseAgentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*manager.ReleaseAgentRequest(nil), f.releaseAgentCalls...)
}

// dialTestManager starts server on an in-process bufconn listener and returns a client
// connection to it, cleaned up automatically at the end of the test.
func dialTestManager(t *testing.T, server manager.ManagerServer) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	manager.RegisterManagerServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() {
		conn.Close()
		grpcServer.Stop()
		listener.Close()
	})
	return conn
}

// newIngestTestSession builds a session usable by session.Ingest/session.LeaveIngest: it has
// a client config in its context, a namespace, and (optionally) a manager connection.
func newIngestTestSession(t *testing.T, managerConn *grpc.ClientConn) *session {
	t.Helper()
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())
	return &session{
		Cluster: &k8s.Cluster{
			Kubeconfig: &k8s.Kubeconfig{
				Context:   ctx,
				Namespace: "default",
			},
		},
		currentIngests: xsync.NewMap[ingestKey, *ingest](),
		ingestTracker:  newPodAccessTracker(),
		managerConn:    managerConn,
		sessionInfo:    &manager.SessionInfo{SessionId: "test-session"},
		managerVersion: semver.Version{Major: 2, Minor: 30, Patch: 0},
	}
}

func TestSession_Ingest_NodeAgentVersionGate(t *testing.T) {
	s := newIngestTestSession(t, nil)
	s.managerVersion = semver.Version{Major: 2, Minor: 29, Patch: 9}

	_, err := s.Ingest(s, &rpc.IngestRequest{
		Identifier: &rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn"},
		NodeAgent:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no support for node-agents")
}

func TestSession_Ingest_CachedSidecarRejectsNodeAgentRequest(t *testing.T) {
	s := newIngestTestSession(t, nil)
	s.currentAgentPods = []agentPod{
		{workload: "wl", namespace: "default", podName: "wl-pod", nodeAgent: false},
	}

	_, err := s.Ingest(s, &rpc.IngestRequest{
		Identifier: &rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn"},
		NodeAgent:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an injected traffic-agent")
}

func TestSession_Ingest_CachedNodeAgentReusedSilentlyForPlainRequest(t *testing.T) {
	fake := &fakeManagerServer{
		ensureAgentResponse: &manager.AgentInfoSnapshot{
			Agents: []*manager.AgentInfo{
				{
					Name:      "wl",
					Namespace: "default",
					NodeAgent: true,
					Containers: map[string]*manager.AgentInfo_ContainerInfo{
						"cn": {},
					},
				},
			},
		},
	}
	s := newIngestTestSession(t, dialTestManager(t, fake))
	withFakeRootDaemon(t, s)
	s.currentAgentPods = []agentPod{
		{workload: "wl", namespace: "default", podName: "wl-pod", nodeAgent: true},
	}

	ii, err := s.Ingest(s, &rpc.IngestRequest{
		Identifier: &rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn"},
		NodeAgent:  false,
	})
	require.NoError(t, err)
	assert.Equal(t, "wl", ii.Workload)

	calls := fake.ensureAgentCalls()
	require.Len(t, calls, 1)
	assert.True(t, calls[0].NodeAgent, "a cached node-agent must be requested explicitly from EnsureAgent so the manager does not inject a sidecar on top of it")
}

func TestSession_Ingest_ResponseHardeningRejectsSidecarFromManager(t *testing.T) {
	fake := &fakeManagerServer{
		ensureAgentResponse: &manager.AgentInfoSnapshot{
			Agents: []*manager.AgentInfo{
				{
					Name:      "wl",
					Namespace: "default",
					NodeAgent: false,
					Containers: map[string]*manager.AgentInfo_ContainerInfo{
						"cn": {},
					},
				},
			},
		},
	}
	s := newIngestTestSession(t, dialTestManager(t, fake))

	_, err := s.Ingest(s, &rpc.IngestRequest{
		Identifier: &rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn"},
		NodeAgent:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not honor node-agent mode")
}

// newTestIngest builds an *ingest whose cancel func mirrors production's cancelIngest
// closure (ingest.go), so LeaveIngest exercises the same teardown sequence.
func newTestIngest(s *session, ik ingestKey, ai *manager.AgentInfo) *ingest {
	ctx, cancel := context.WithCancel(context.Background())
	ig := &ingest{ingestKey: ik, AgentInfo: ai, ctx: ctx}
	ig.cancel = func() {
		s.currentIngests.Delete(ik)
		cancel()
		s.ingestTracker.cancelContainer(ik.workload, ik.container)
	}
	return ig
}

func TestSession_LeaveIngest_ReleasesNodeAgentWhenLastForWorkload(t *testing.T) {
	proc.SetRunningInContainer(false)

	fake := &fakeManagerServer{}
	s := newIngestTestSession(t, dialTestManager(t, fake))

	ik := ingestKey{workload: "wl", container: "cn", namespace: "default"}
	ai := &manager.AgentInfo{
		Name:      "wl",
		Namespace: "default",
		NodeAgent: true,
		Containers: map[string]*manager.AgentInfo_ContainerInfo{
			"cn": {},
		},
	}
	s.currentIngests.Store(ik, newTestIngest(s, ik, ai))

	_, err := s.LeaveIngest(&rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn", Namespace: "default"})
	require.NoError(t, err)

	calls := fake.releaseCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "wl", calls[0].Name)
	assert.Equal(t, "default", calls[0].Namespace)
	assert.Equal(t, "test-session", calls[0].Session.GetSessionId())

	_, loaded := s.currentIngests.Load(ik)
	assert.False(t, loaded)
}

func TestSession_LeaveIngest_KeepsNodeAgentWhenSiblingContainerRemains(t *testing.T) {
	proc.SetRunningInContainer(false)

	fake := &fakeManagerServer{}
	s := newIngestTestSession(t, dialTestManager(t, fake))

	ai := &manager.AgentInfo{
		Name:      "wl",
		Namespace: "default",
		NodeAgent: true,
		Containers: map[string]*manager.AgentInfo_ContainerInfo{
			"cn1": {},
			"cn2": {},
		},
	}
	ik1 := ingestKey{workload: "wl", container: "cn1", namespace: "default"}
	ik2 := ingestKey{workload: "wl", container: "cn2", namespace: "default"}
	s.currentIngests.Store(ik1, newTestIngest(s, ik1, ai))
	s.currentIngests.Store(ik2, newTestIngest(s, ik2, ai))

	_, err := s.LeaveIngest(&rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn1", Namespace: "default"})
	require.NoError(t, err)

	assert.Empty(t, fake.releaseCalls())

	_, loaded := s.currentIngests.Load(ik2)
	assert.True(t, loaded)
}

func TestSession_LeaveIngest_DoesNotReleaseSidecarAgent(t *testing.T) {
	proc.SetRunningInContainer(false)

	fake := &fakeManagerServer{}
	s := newIngestTestSession(t, dialTestManager(t, fake))

	ik := ingestKey{workload: "wl", container: "cn", namespace: "default"}
	ai := &manager.AgentInfo{
		Name:      "wl",
		Namespace: "default",
		NodeAgent: false,
		Containers: map[string]*manager.AgentInfo_ContainerInfo{
			"cn": {},
		},
	}
	s.currentIngests.Store(ik, newTestIngest(s, ik, ai))

	_, err := s.LeaveIngest(&rpc.IngestIdentifier{WorkloadName: "wl", ContainerName: "cn", Namespace: "default"})
	require.NoError(t, err)

	assert.Empty(t, fake.releaseCalls())
}
