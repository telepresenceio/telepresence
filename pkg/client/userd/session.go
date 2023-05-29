package userd

import (
	"context"

	"google.golang.org/grpc"
	core "k8s.io/api/core/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dgroup"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type WatchWorkloadsStream interface {
	Send(*rpc.WorkloadInfoSnapshot) error
}

type InterceptInfo interface {
	APIKey() string
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

	// As will cast this instance to what the given ptr points to, and assign
	// that to the pointer. It will panic if type is not implemented.
	As(ptr any)

	InterceptProlog(context.Context, *manager.CreateInterceptRequest) *rpc.InterceptResult
	InterceptEpilog(context.Context, *rpc.CreateInterceptRequest, *rpc.InterceptResult) *rpc.InterceptResult
	RemoveIntercept(context.Context, string) error

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

	Status(context.Context) *rpc.ConnectInfo
	UpdateStatus(context.Context, *rpc.ConnectRequest) *rpc.ConnectInfo

	Uninstall(context.Context, *rpc.UninstallRequest) (*common.Result, error)

	WatchWorkloads(context.Context, *rpc.WatchWorkloadsRequest, WatchWorkloadsStream) error
	WorkloadInfoSnapshot(context.Context, []string, rpc.ListRequest_Filter, bool) (*rpc.WorkloadInfoSnapshot, error)

	GetCurrentNamespaces(forClientAccess bool) []string
	ActualNamespace(string) string
	AddNamespaceListener(context.Context, NamespaceListener)

	WithK8sInterface(context.Context) context.Context
	ForeachAgentPod(ctx context.Context, fn func(context.Context, typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error

	GatherLogs(context.Context, *connector.LogsRequest) (*connector.LogsResponse, error)
	GatherTraces(ctx context.Context, tr *connector.TracesRequest) *common.Result

	Reporter() *scout.Reporter
	SessionInfo() *manager.SessionInfo
	RootDaemon() rootdRpc.DaemonClient

	ApplyConfig(context.Context) error
	GetConfig(context.Context) (*client.SessionConfig, error)
	StartServices(g *dgroup.Group)
	Epilog(ctx context.Context)
	Done() <-chan struct{}
}

type NewSessionFunc func(context.Context, *scout.Reporter, *rpc.ConnectRequest, *client.Kubeconfig) (context.Context, Session, *connector.ConnectInfo)

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

// RunSession (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func RunSession(c context.Context, s Session) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	defer func() {
		s.Epilog(c)
	}()
	s.StartServices(g)
	return g.Wait()
}
