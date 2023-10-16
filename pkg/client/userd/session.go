package userd

import (
	"context"

	"github.com/blang/semver"
	"google.golang.org/grpc"
	core "k8s.io/api/core/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type WatchWorkloadsStream interface {
	Send(*rpc.WorkloadInfoSnapshot) error
	Context() context.Context
}

type InterceptInfo interface {
	InterceptResult() *rpc.InterceptResult
	PreparedIntercept() *manager.PreparedIntercept
	PortIdentifier() (agentconfig.PortIdentifier, error)
}

type KubeConfig interface {
	GetContext() string
	GetRestConfig() *rest.Config
	GetManagerNamespace() string
}

type NamespaceListener func(context.Context)

type Session interface {
	restapi.AgentState
	KubeConfig

	AddIntercept(context.Context, *rpc.CreateInterceptRequest) *rpc.InterceptResult
	CanIntercept(context.Context, *rpc.CreateInterceptRequest) (InterceptInfo, *rpc.InterceptResult)
	InterceptProlog(context.Context, *manager.CreateInterceptRequest) *rpc.InterceptResult
	InterceptEpilog(context.Context, *rpc.CreateInterceptRequest, *rpc.InterceptResult) *rpc.InterceptResult
	RemoveIntercept(context.Context, string) error
	NewCreateInterceptRequest(*manager.InterceptSpec) *manager.CreateInterceptRequest

	AddInterceptor(string, *rpc.Interceptor) error
	RemoveInterceptor(string) error
	ClearIntercepts(context.Context) error

	GetInterceptInfo(string) *manager.InterceptInfo
	GetInterceptSpec(string) *manager.InterceptSpec
	InterceptsForWorkload(string, string) []*manager.InterceptSpec

	ManagerClient() manager.ManagerClient
	ManagerConn() *grpc.ClientConn
	ManagerName() string
	ManagerVersion() semver.Version
	NewRemainRequest() *manager.RemainRequest

	Status(context.Context) *rpc.ConnectInfo
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo

	Uninstall(context.Context, *rpc.UninstallRequest) (*common.Result, error)

	WatchWorkloads(context.Context, *rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WorkloadInfoSnapshot(context.Context, []string, rpc.ListRequest_Filter) (*rpc.WorkloadInfoSnapshot, error)

	GetCurrentNamespaces(forClientAccess bool) []string
	ActualNamespace(string) string
	AddNamespaceListener(context.Context, NamespaceListener)

	WithK8sInterface(context.Context) context.Context
	ForeachAgentPod(ctx context.Context, fn func(context.Context, typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error

	GatherLogs(context.Context, *connector.LogsRequest) (*connector.LogsResponse, error)
	GatherTraces(ctx context.Context, tr *connector.TracesRequest) *common.Result

	SessionInfo() *manager.SessionInfo
	RootDaemon() rootdRpc.DaemonClient

	ApplyConfig(context.Context) error
	GetConfig(context.Context) (*client.SessionConfig, error)
	RunSession(c context.Context) error
	StartServices(g *dgroup.Group)
	Remain(ctx context.Context) error
	Epilog(ctx context.Context)
	Done() <-chan struct{}
}

type NewSessionFunc func(context.Context, *rpc.ConnectRequest, *client.Kubeconfig) (context.Context, Session, *connector.ConnectInfo)

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
