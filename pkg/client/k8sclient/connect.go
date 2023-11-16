package k8sclient

import (
	"context"
	"fmt"
	"net"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

func ConnectToManager(ctx context.Context, namespace string, grpcDialer dnet.DialerFunc) (*grpc.ClientConn, manager.ManagerClient, *manager.VersionInfo2, error) {
	grpcAddr := net.JoinHostPort("svc/traffic-manager."+namespace, "api")
	conn, err := dialClusterGRPC(ctx, grpcAddr, grpcDialer)
	if err != nil {
		return nil, nil, nil, client.CheckTimeout(ctx, fmt.Errorf("dial manager: %w", err))
	}
	mClient := manager.NewManagerClient(conn)
	vi, err := getVersion(ctx, mClient)
	if err != nil {
		conn.Close()
	}
	return conn, mClient, vi, nil
}

type versionAPI interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*manager.VersionInfo2, error)
}

func ConnectToAgent(
	ctx context.Context,
	grpcDialer dnet.DialerFunc,
	podName, namespace string,
	port uint16,
) (*grpc.ClientConn, agent.AgentClient, *manager.VersionInfo2, error) {
	grpcAddr := fmt.Sprintf("pod/%s.%s:%d", podName, namespace, port)
	conn, err := dialClusterGRPC(ctx, grpcAddr, grpcDialer)
	if err != nil {
		return nil, nil, nil, client.CheckTimeout(ctx, fmt.Errorf("dial agent: %w", err))
	}
	mClient := agent.NewAgentClient(conn)
	vi, err := getVersion(ctx, mClient)
	if err != nil {
		conn.Close()
	}
	return conn, mClient, vi, nil
}

func dialClusterGRPC(ctx context.Context, address string, grpcDialer dnet.DialerFunc) (*grpc.ClientConn, error) {
	return grpc.DialContext(ctx, address, grpc.WithContextDialer(grpcDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
}

func getVersion(ctx context.Context, gc versionAPI) (*manager.VersionInfo2, error) {
	// At this point, we are connected to the traffic-manager. We use the shorter API timeout
	tos := client.GetConfig(ctx).Timeouts()
	ctx, cancelAPI := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancelAPI()

	vi, err := gc.Version(ctx, &empty.Empty{})
	if err != nil {
		err = client.CheckTimeout(ctx, fmt.Errorf("get version: %w", err))
	} else {
		dlog.Infof(ctx, "Connected to %s %s", vi.Name, vi.Version)
	}
	return vi, err
}
