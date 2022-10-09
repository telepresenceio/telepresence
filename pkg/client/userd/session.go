package userd

import (
	"context"

	"google.golang.org/grpc"
	core "k8s.io/api/core/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	rpc2 "github.com/datawire/go-fuseftp/rpc"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type WatchWorkloadsStream interface {
	Send(*rpc.WorkloadInfoSnapshot) error
}

type ServiceProps interface {
	APIKey() string
	InterceptResult() *rpc.InterceptResult
	PreparedIntercept() *manager.PreparedIntercept
	PortIdentifier() (agentconfig.PortIdentifier, error)
}

type KubeConfig interface {
	GetRestConfig() *rest.Config
	GetManagerNamespace() string
}

type NamespaceListener func(context.Context)

type Session interface {
	restapi.AgentState
	KubeConfig
	AddIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptResult, error)
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (ServiceProps, *rpc.InterceptResult)
	AddInterceptor(string, int) error
	RemoveInterceptor(string) error
	GetInterceptSpec(string) *manager.InterceptSpec
	InterceptsForWorkload(string, string) []*manager.InterceptSpec
	Status(context.Context) *rpc.ConnectInfo
	ClearIntercepts(context.Context) error
	RemoveIntercept(context.Context, string) error
	Run(context.Context) error
	Uninstall(context.Context, *rpc.UninstallRequest) (*rpc.Result, error)
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo
	WatchWorkloads(context.Context, *rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WithK8sInterface(context.Context) context.Context
	WorkloadInfoSnapshot(context.Context, []string, rpc.ListRequest_Filter, bool) (*rpc.WorkloadInfoSnapshot, error)
	ManagerClient() manager.ManagerClient
	ManagerConn() *grpc.ClientConn
	GetCurrentNamespaces(forClientAccess bool) []string
	ActualNamespace(string) string
	AddNamespaceListener(context.Context, NamespaceListener)
	GatherLogs(context.Context, *connector.LogsRequest) (*connector.LogsResponse, error)
	ForeachAgentPod(ctx context.Context, fn func(context.Context, typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error
	GatherTraces(ctx context.Context, tr *connector.TracesRequest) *connector.Result
}

type NewSessionFunc func(context.Context, *scout.Reporter, *rpc.ConnectRequest, Service, rpc2.FuseFTPClient) (context.Context, Session, *connector.ConnectInfo)

type newSessionKey struct{}

func WithNewSessionFunc(ctx context.Context, f NewSessionFunc) context.Context {
	return context.WithValue(ctx, newSessionKey{}, f)
}

func GetNewSessionFunc(ctx context.Context) NewSessionFunc {
	if f, ok := ctx.Value(newSessionKey{}).(NewSessionFunc); ok {
		return f
	}
	panic("No User daemon Session creator has been registered")
}
