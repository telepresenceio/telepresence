package k8s

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/blang/semver/v4"
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
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
)

func (kc *Cluster) ConnectToManager(dialCtx context.Context, namespace string) (conn *grpc.ClientConn, name string, ver semver.Version, err error) {
	grpcAddr := net.JoinHostPort("svc/traffic-manager."+namespace, "api")

	dialCtx, cancel := client.GetConfig(kc).Timeouts().TimeoutContext(dialCtx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	pap, err := portforward.ResolveSvcToPod(kc, "traffic-manager", namespace, "8081")
	if err != nil {
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				return nil, "", ver, errcat.User.New("traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in.")
			}
		}
		return nil, "", semver.Version{}, err
	}

	conn, err = kc.dialGRPC(dialCtx, grpcAddr, pap)
	if err != nil {
		return nil, "", ver, err
	}
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			dlog.Infof(kc, "Connected to Manager %s", ver)
		}
	}()

	vi, err := getVersion(dialCtx, manager.NewManagerClient(conn))
	if err != nil {
		return conn, "", ver, client.CheckTimeout(dialCtx, fmt.Errorf("dial manager: %w", err))
	}
	verStr := strings.TrimPrefix(vi.Version, "v")
	ver, err = semver.Parse(verStr)
	if err != nil {
		err = fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}
	return conn, vi.Name, ver, err
}

type versionAPI interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*manager.VersionInfo2, error)
}

func (kc *Cluster) ConnectToAgent(
	dialCtx context.Context,
	podName string,
	port uint16,
	podID types.UID,
) (*grpc.ClientConn, agent.AgentClient, *manager.VersionInfo2, error) {
	var grpcAddr string
	if podID == "" {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d", podName, kc.Namespace, port)
	} else {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d#%s", podName, kc.Namespace, port, podID)
	}
	conn, err := kc.dialGRPC(dialCtx, grpcAddr, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	mClient := agent.NewAgentClient(conn)
	vi, err := getVersion(dialCtx, mClient)
	if err != nil {
		err = client.CheckTimeout(dialCtx, fmt.Errorf("dial agent: %w", err))
		conn.Close()
	}
	return conn, mClient, vi, err
}

func (kc *Cluster) dialGRPC(dialCtx context.Context, address string, knownPod *portforward.PodAddress) (*grpc.ClientConn, error) {
	return grpcClient.DialGRPC(dialCtx, portforward.K8sPFScheme+":///"+address, grpc.WithContextDialer(portforward.Dialer(kc)),
		grpc.WithResolvers(portforward.NewResolver(kc, knownPod)),
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
