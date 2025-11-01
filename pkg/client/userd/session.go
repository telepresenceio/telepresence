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
	context.Context
	GetKubeContext() string
	GetRestConfig() *rest.Config
	GetClientConfig() clientcmd.ClientConfig
}

type Session interface {
	KubeConfig
	restapi.AgentState
	tunnel.SyntheticIPResolver
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) *rpc.InterceptResult
	AddInterceptor(string, *rpc.Interceptor) error
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (InterceptInfo, *rpc.InterceptResult)
	ClearIngestsAndIntercepts() error
	GatherLogs(context.Context, *rpc.LogsRequest) (*rpc.LogsResponse, error)
	GetConfig() (*client.SessionConfig, error)
	GetCurrentNamespaces(forClientAccess bool) []string
	GetIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
	GetInterceptInfo(string) *manager.InterceptInfo
	GetInterceptSpec(string) *manager.InterceptSpec
	GetService() Service
	Ingest(context.Context, *rpc.IngestRequest) (*rpc.IngestInfo, error)
	InterceptsForWorkload(string, string) []*manager.InterceptSpec
	LeaveIngest(*rpc.IngestIdentifier) (*rpc.IngestInfo, error)
	ManagerClient() manager.ManagerClient
	ManagerName() string
	ManagerVersion() semver.Version
	RemoveIntercept(string) error
	RemoveInterceptor(string) error
	RerouteLocalPort(ap types.AddrPortProto, srcPort uint16)

	// WithRootClient calls the given function with a gRPC-cancel sensitive context and the root daemon client.
	// This function is intended to facilitate gRPC calls to the root daemon that are sensitive to both the session
	// context and the context from the originating gRPC call to the user daemon.
	WithRootClient(context.Context, func(context.Context, rootdRpc.DaemonClient) error) error

	Run()
	SessionInfo() *manager.SessionInfo
	Status(context.Context) *rpc.ConnectInfo
	Uninstall(context.Context, *rpc.UninstallRequest) (*common.Result, error)
	CheckStatus(request *rpc.ConnectRequest) *rpc.ConnectInfo
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo
	WatchWorkloads(*rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WorkloadInfoSnapshot([]string, rpc.ListRequest_Filter) (*rpc.WorkloadInfoSnapshot, error)
}
