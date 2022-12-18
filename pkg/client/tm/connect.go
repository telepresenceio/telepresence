package tm

import (
	"context"
	"fmt"
	"net"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

func ConnectToManager(ctx context.Context, namespace string, grpcDialer dnet.DialerFunc) (*grpc.ClientConn, manager.ManagerClient, *manager.VersionInfo2, error) {
	grpcAddr := net.JoinHostPort("svc/traffic-manager."+namespace, "api")

	// First check. Establish connection
	opts := []grpc.DialOption{
		grpc.WithContextDialer(grpcDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	}

	conn, err := grpc.DialContext(ctx, grpcAddr, opts...)
	if err != nil {
		return nil, nil, nil, client.CheckTimeout(ctx, fmt.Errorf("dial manager: %w", err))
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()

	clientConfig := client.GetConfig(ctx)
	tos := &clientConfig.Timeouts
	mClient := manager.NewManagerClient(conn)

	// At this point, we are connected to the traffic-manager. We use the shorter API timeout
	ctx, cancelAPI := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancelAPI()

	vi, err := mClient.Version(ctx, &empty.Empty{})
	if err != nil {
		return nil, nil, nil, client.CheckTimeout(ctx, fmt.Errorf("manager.Version: %w", err))
	}
	dlog.Infof(ctx, "Connected to traffic-manager %s", vi.Version)
	return conn, mClient, vi, nil
}
