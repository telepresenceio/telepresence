package k8sclient

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
)

func ConnectToManager(ctx context.Context, namespace string) (*grpc.ClientConn, manager.ManagerClient, *manager.VersionInfo2, error) {
	grpcAddr := net.JoinHostPort("svc/traffic-manager."+namespace, "api")
	conn, err := dialClusterGRPC(ctx, grpcAddr)
	if err != nil {
		return nil, nil, nil, err
	}
	mClient := manager.NewManagerClient(conn)
	vi, err := getVersion(ctx, mClient)
	if err != nil {
		err = client.CheckTimeout(ctx, fmt.Errorf("dial manager: %w", err))
		conn.Close()
	}
	return conn, mClient, vi, err
}

type versionAPI interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*manager.VersionInfo2, error)
}

func ConnectToAgent(
	ctx context.Context,
	podName, namespace string,
	port uint16,
	podID types.UID,
) (*grpc.ClientConn, agent.AgentClient, *manager.VersionInfo2, error) {
	var grpcAddr string
	if podID == "" {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d", podName, namespace, port)
	} else {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d#%s", podName, namespace, port, podID)
	}
	conn, err := dialClusterGRPC(ctx, grpcAddr)
	if err != nil {
		return nil, nil, nil, err
	}
	mClient := agent.NewAgentClient(conn)
	vi, err := getVersion(ctx, mClient)
	if err != nil {
		err = client.CheckTimeout(ctx, fmt.Errorf("dial agent: %w", err))
		conn.Close()
	}
	return conn, mClient, vi, err
}

func dialClusterGRPC(ctx context.Context, address string) (*grpc.ClientConn, error) {
	return grpc.NewClient(portforward.K8sPFScheme+":///"+address, grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func getVersion(ctx context.Context, gc versionAPI) (*manager.VersionInfo2, error) {
	// At this point, we are connected to the traffic-manager. We use the shorter API timeout
	tos := client.GetConfig(ctx).Timeouts()
	b := backoff.ExponentialBackOff{
		InitialInterval:     500 * time.Millisecond,
		RandomizationFactor: backoff.DefaultRandomizationFactor,
		Multiplier:          backoff.DefaultMultiplier,
		MaxInterval:         2 * time.Second,
		MaxElapsedTime:      tos.Get(client.TimeoutTrafficManagerAPI),
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}
	b.Reset()
	var vi *manager.VersionInfo2
	err := backoff.Retry(func() (err error) {
		vi, err = gc.Version(ctx, &empty.Empty{})
		return err
	}, &b)
	if err == nil {
		dlog.Infof(ctx, "Connected to %s %s", vi.Name, vi.Version)
	}
	return vi, err
}
