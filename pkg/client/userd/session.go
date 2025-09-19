package userd

import (
	"context"

	"github.com/blang/semver/v4"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type ConnectRequest interface {
	Request() *rpc.ConnectRequest
}

type WatchWorkloadsStream interface {
	Send(*rpc.WorkloadInfoSnapshot) error
	Context() context.Context
}

type InterceptInfo interface {
	InterceptResult() *rpc.InterceptResult
	PreparedIntercept() *manager.PreparedIntercept
	PortIdentifier() (types.PortIdentifier, error)
}

type KubeConfig interface {
	GetContext() string
	GetRestConfig() *rest.Config
	GetClientConfig() clientcmd.ClientConfig
}

type NamespaceListener func(context.Context)

type Session interface {
	restapi.AgentState
	KubeConfig
	tunnel.SyntheticIPResolver
	AddIntercept(*rpc.CreateInterceptRequest) *rpc.InterceptResult
	AddInterceptor(string, *rpc.Interceptor) error
	ApplyConfig() error
	Cancel()
	CanIntercept(*rpc.CreateInterceptRequest) (InterceptInfo, *rpc.InterceptResult)
	ClearIngestsAndIntercepts() error
	Done() <-chan struct{}
	GatherLogs(*rpc.LogsRequest) (*rpc.LogsResponse, error)
	GetConfig() (*client.SessionConfig, error)
	GetCurrentNamespaces(forClientAccess bool) []string
	GetIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
	GetInterceptInfo(string) *manager.InterceptInfo
	GetInterceptSpec(string) *manager.InterceptSpec
	Ingest(*rpc.IngestRequest) (*rpc.IngestInfo, error)
	InterceptsForWorkload(string, string) []*manager.InterceptSpec
	LeaveIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
	ManagerClient() manager.ManagerClient
	ManagerName() string
	ManagerVersion() semver.Version
	RemoveIntercept(string) error
	RemoveInterceptor(string) error
	RerouteLocalPort(ap types.AddrPortProto, srcPort uint16)
	RootDaemon() rootdRpc.DaemonClient
	Run() error
	SessionInfo() *manager.SessionInfo
	Status() *rpc.ConnectInfo
	Uninstall(*rpc.UninstallRequest) (*common.Result, error)
	UpdateStatus(ConnectRequest) *rpc.ConnectInfo
	WatchWorkloads(*rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WorkloadInfoSnapshot([]string, rpc.ListRequest_Filter) (*rpc.WorkloadInfoSnapshot, error)

	/*
		AddIntercept(*rpc.CreateInterceptRequest) *rpc.InterceptResult
		CanIntercept(*rpc.CreateInterceptRequest) (InterceptInfo, *rpc.InterceptResult)
		RemoveIntercept(string) error
		NewCreateInterceptRequest(*manager.InterceptSpec) *manager.CreateInterceptRequest

		AddInterceptor(string, *rpc.Interceptor) error
		RemoveInterceptor(string) error
		ClearIngestsAndIntercepts() error

		GetInterceptInfo(string) *manager.InterceptInfo
		InterceptsForWorkload(string, string) []*manager.InterceptSpec

		ManagerClient() manager.ManagerClient
		ManagerConn() *grpc.ClientConn
		ManagerName() string
		ManagerVersion() semver.Version
		NewRemainRequest() *manager.RemainRequest

		Status() *rpc.ConnectInfo
		UpdateStatus(ConnectRequest) *rpc.ConnectInfo

		Uninstall(*rpc.UninstallRequest) (*common.Result, error)

		WatchWorkloads(*rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error

		GetCurrentNamespaces(forClientAccess bool) []string
		ActualNamespace(string) string
		AddNamespaceListener(context.Context, NamespaceListener)

		WithJoinedClientSetInterface(context.Context) context.Context
		ForeachAgentPod(fn func(typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error

		GatherLogs(*rpc.LogsRequest) (*rpc.LogsResponse, error)

		SessionInfo() *manager.SessionInfo
		RootDaemon() rootdRpc.DaemonClient

		ApplyConfig() error
		GetConfig() (*client.SessionConfig, error)
		Run() error
		StartServices(g *dgroup.Group)
		Cancel()
		Remain() error
		Epilogue()
		Done() <-chan struct{}
		Ingest(*rpc.IngestRequest) (*rpc.IngestInfo, error)
		GetIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
		LeaveIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
		RerouteLocalPort(ap types.AddrPortProto, srcPort uint16)
	*/
}
