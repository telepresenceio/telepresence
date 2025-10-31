package k8s

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	empty "google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func ConnectToManager(longLivedCtx, ctx context.Context, namespace string) (conn *grpc.ClientConn, mc manager.ManagerClient, vi *manager.VersionInfo2, err error) {
	grpcAddr := net.JoinHostPort("svc/traffic-manager."+namespace, "api")

	dialCtx, cancel := client.GetConfig(longLivedCtx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	pap, err := portforward.ResolveSvcToPod(ctx, "traffic-manager", namespace, "8081")
	if err != nil {
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				return nil, nil, nil, errcat.User.New("traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in.")
			}
		}
		return nil, nil, nil, err
	}

	conn, err = dialClusterGRPC(dialCtx, grpcAddr, pap)
	if err != nil {
		return nil, nil, nil, err
	}
	mClient := manager.NewManagerClient(conn)
	vi, err = getVersion(ctx, mClient)
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
	longLivedCtx context.Context,
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
	conn, err := dialClusterGRPC(longLivedCtx, grpcAddr, nil)
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

func dialClusterGRPC(ctx context.Context, address string, knownPod *portforward.PodAddress) (*grpc.ClientConn, error) {
	return grpc.NewClient(portforward.K8sPFScheme+":///"+address, grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithResolvers(portforward.NewResolver(ctx, knownPod)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 24 * time.Hour, Timeout: 20 * time.Second}),
		grpc.WithIdleTimeout(0),
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
	}, backoff.WithContext(&b, ctx))
	if err == nil {
		dlog.Infof(ctx, "Connected to %s %s", vi.Name, vi.Version)
	}
	return vi, err
}
