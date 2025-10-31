package daemon

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blang/semver/v4"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

type UserClient interface {
	connector.ConnectorClient
	io.Closer
	Conn() *grpc.ClientConn
	Containerized() bool
	DaemonPort() int
	DaemonID() *Identifier
	Executable() string
	DaemonInfo() *Info
	Lookup(ctx context.Context, addr string) (netip.Addr, error)
	Name() string
	Semver() semver.Version
	AddHandler(ctx context.Context, id string, cmd *exec.Cmd, containerName string) error
	SetConnectionInfo(name string, clusterContext string, namespace string)
}

type userClient struct {
	connector.ConnectorClient
	conn       *grpc.ClientConn
	info       *Info
	version    semver.Version
	executable string
	name       string
}

var NewUserClientFunc = NewUserClient //nolint:gochecknoglobals // extension point

func NewUserClient(conn *grpc.ClientConn, info *Info, version semver.Version, name string, executable string) UserClient {
	return &userClient{ConnectorClient: connector.NewConnectorClient(conn), conn: conn, info: info, version: version, name: name, executable: executable}
}

type Session struct {
	UserClient
	Info    *connector.ConnectInfo
	Started bool
}

type userDaemonKey struct{}

func GetUserClient(ctx context.Context) UserClient {
	if ud, ok := ctx.Value(userDaemonKey{}).(UserClient); ok {
		return ud
	}
	return nil
}

func MustGetUserClient(ctx context.Context) UserClient {
	ud := GetUserClient(ctx)
	if ud == nil {
		panic("no user client in context")
	}
	return ud
}

func WithUserClient(ctx context.Context, ud UserClient) context.Context {
	return context.WithValue(ctx, userDaemonKey{}, ud)
}

type sessionKey struct{}

func GetSession(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return s
	}
	return nil
}

func MustGetSession(ctx context.Context) *Session {
	s := GetSession(ctx)
	if s == nil {
		panic("no session in context")
	}
	return s
}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func (u *userClient) Close() error {
	return u.conn.Close()
}

func (u *userClient) Conn() *grpc.ClientConn {
	return u.conn
}

func (u *userClient) DaemonInfo() *Info {
	return u.info
}

func (u *userClient) Containerized() bool {
	return u.info != nil && u.info.InDocker()
}

func (u *userClient) DaemonID() *Identifier {
	return u.info.DaemonID()
}

func (u *userClient) Executable() string {
	return u.executable
}

func (u *userClient) Lookup(ctx context.Context, name string) (addr netip.Addr, err error) {
	ipb, err := u.LookupIP(ctx, &daemon.LookupIPRequest{Name: name})
	if err != nil {
		return addr, errcat.User.Newf("unable to resolve name %q: %v", name, err)
	}
	err = addr.UnmarshalBinary(ipb.Ip)
	if err != nil {
		return addr, errcat.NoDaemonLogs.New(err)
	}
	return addr, nil
}

func (u *userClient) Name() string {
	return u.name
}

func (u *userClient) Semver() semver.Version {
	return u.version
}

func (u *userClient) DaemonPort() int {
	if u.info.InDocker() {
		addr := u.conn.Target()
		if lc := strings.LastIndexByte(addr, ':'); lc >= 0 {
			if port, err := strconv.Atoi(addr[lc+1:]); err == nil {
				return port
			}
		}
	}
	return -1
}

func (u *userClient) SetConnectionInfo(name string, clusterContext string, namespace string) {
	u.info.SetConnectionInfo(name, clusterContext, namespace)
}

func (u *userClient) AddHandler(ctx context.Context, id string, cmd *exec.Cmd, containerName string) error {
	// setup cleanup for the handler process
	ior := connector.Interceptor{
		InterceptId:   id,
		Pid:           int32(cmd.Process.Pid),
		ContainerName: containerName,
	}

	// Send info about the pid and intercept id to the traffic-manager so that it kills
	// the process if it receives a leave of quit call.
	if _, err := u.AddInterceptor(ctx, &ior); err != nil {
		switch grpcStatus.Code(err) {
		case grpcCodes.NotFound, grpcCodes.Canceled:
			// The intercept was already deleted or deactivation was caused by a disconnect
			dlog.Infof(ctx, "intercept no longer present when adding container %s as interceptor", containerName)
			err = nil
		default:
			dlog.Errorf(ctx, "error adding process with pid %d as interceptor: %v", ior.Pid, err)
		}
		_ = cmd.Process.Kill()
		return err
	}
	return nil
}

func (s *Session) GetAgentConfig(ctx context.Context, workload string) (*agentconfig.Sidecar, error) {
	agc, err := s.UserClient.GetAgentConfig(ctx, &manager.AgentConfigRequest{Name: workload})
	if err != nil {
		return nil, err
	}
	return agentconfig.UnmarshalYAML(agc.Data)
}

func (s *Session) GetRootClientConfig() (client.Config, error) {
	return GetRootClientConfig(s.Info.GetDaemonStatus())
}

func GetRootClientConfig(ds *daemon.DaemonStatus) (client.Config, error) {
	data := ds.GetOutboundConfig().GetClientConfig()
	if data == nil {
		return nil, errors.New("no outbound config")
	}
	cfg := client.GetDefaultConfig()
	if err := json.Unmarshal(data, cfg, true); err != nil {
		return nil, err
	}
	return cfg, nil
}

// GetCommandKubeConfig will return the fully resolved client.Kubeconfig for the given command.
func GetCommandKubeConfig(cmd *cobra.Command) (context.Context, *k8s.Kubeconfig, error) {
	ctx := cmd.Context()
	uc := GetUserClient(ctx)
	var kc *k8s.Kubeconfig
	var err error
	if uc != nil && !cmd.Flag("context").Changed {
		// Get the context that we're currently connected to.
		var ci *connector.ConnectInfo
		ci, err = uc.Status(ctx, &emptypb.Empty{})
		if err == nil {
			ctx, kc, err = k8s.NewKubeconfig(ctx, map[string]string{"context": ci.ClusterContext}, "")
		}
	} else {
		if GetRequest(ctx) == nil {
			if ctx, err = WithDefaultRequest(cmd); err != nil {
				return ctx, nil, err
			}
		}
		rq := GetRequest(ctx)
		ctx, kc, err = k8s.NewKubeconfig(ctx, rq.KubeFlags, rq.ManagerNamespace)
	}
	return ctx, kc, err
}
