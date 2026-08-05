package k8s

import (
	"context"
	"errors"
	"fmt"
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

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
)

func (kc *Cluster) ConnectToManager(dialCtx context.Context, namespace string) (conn *grpc.ClientConn, name string, ver semver.Version, err error) {
	dialCtx, cancel := client.GetConfig(kc).Timeouts().TimeoutContext(dialCtx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	pap, err := portforward.ResolveSvcToPod(kc, "traffic-manager", namespace, "8081")
	if err != nil {
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				return nil, "", ver, errcat.User.New("traffic manager not found, if it is not installed, please run 'telepresence setup' to configure and install it, " +
					"or 'telepresence helm install' for a plain install. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in.")
			}
		}
		return nil, "", semver.Version{}, err
	}

	// The connection is pinned to the resolved pod for its entire lifetime.
	// When the pod goes away, the connection dies with it, and the session's
	// reconnect logic establishes a new connection against a fresh
	// resolution.
	grpcAddr := fmt.Sprintf("pod/%s.%s:%d#%s", pap.Name, pap.Namespace, pap.Port, pap.PodID)

	bearerSrc := newManagerTokenSource(kc.Kubeconfig)
	x509Src := newX509TokenSource(kc.Kubeconfig)
	hasBearerSource := bearerSrc != nil
	hasX509Source := x509Src != nil

	var extra []grpc.DialOption
	switch {
	case hasBearerSource && hasX509Source:
		clog.Debugf(kc, "manager calls will carry the kubeconfig's bearer credentials, falling back to x509 client-certificate credentials")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(&managerAuthTokenSource{bearer: bearerSrc, x509: x509Src})))
	case hasBearerSource:
		clog.Debugf(kc, "manager calls will carry the kubeconfig's bearer credentials")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(bearerSrc)))
	case hasX509Source:
		clog.Debugf(kc, "manager calls will carry x509 client-certificate credentials, if the manager supports it")
		extra = append(extra, grpc.WithPerRPCCredentials(newManagerTokenCredentials(x509Src)))
	default:
		clog.Debugf(kc, "the kubeconfig yields no bearer token or client certificate for the traffic-manager connection")
	}
	conn, err = kc.dialGRPC(dialCtx, grpcAddr, extra...)
	if err != nil {
		return nil, "", ver, err
	}
	defer func() {
		if err != nil {
			conn.Close()
		} else {
			clog.Infof(kc, "Connected to Manager %s", ver)
		}
	}()

	vi, err := getVersion(dialCtx, manager.NewManagerClient(conn))
	if err != nil {
		return conn, "", ver, client.CheckTimeout(dialCtx, fmt.Errorf("dial manager: %w", err))
	}

	hasX509Path := false
	if hasX509Source {
		if authPort := vi.GetAuthX509Port(); authPort != 0 {
			// The exchange targets the same pinned pod, over the same shared
			// per-pod stream connection as the gRPC channel, so the pod that
			// mints the token is the pod that receives it.
			x509Src.activate(portforward.Dialer(kc),
				fmt.Sprintf("pod/%s.%s:%d#%s", pap.Name, pap.Namespace, authPort, pap.PodID))
			hasX509Path = true
			clog.Debugf(kc, "manager calls will carry x509 client-certificate credentials via the manager's auth port %d", authPort)
		}
	}

	if err = managerAuthError(vi, hasBearerSource, hasX509Path); err != nil {
		return conn, "", ver, err
	}
	if vi.GetAuthSupported() && !vi.GetAuthRequired() && !hasBearerSource && !hasX509Path {
		clog.Debugf(kc, "traffic-manager %s supports authentication, but the current kubeconfig yields no bearer token or usable client certificate", vi.GetName())
	}
	verStr := strings.TrimPrefix(vi.Version, "v")
	ver, err = semver.Parse(verStr)
	if err != nil {
		err = fmt.Errorf("failed to parse manager version %q: %w", verStr, err)
	}
	return conn, vi.Name, ver, err
}

// managerAuthError returns a user-facing error when vi reports that the
// manager requires an authenticated client but neither a bearer token nor
// an x509 client-certificate path (hasX509Path: a client certificate plus a
// manager-advertised auth port) is available. It returns nil when no error
// applies.
func managerAuthError(vi *manager.VersionInfo2, hasBearerSource, hasX509Path bool) error {
	if !vi.GetAuthRequired() || hasBearerSource || hasX509Path {
		return nil
	}
	return errcat.User.Newf(
		"traffic-manager %s requires an authenticated client, but the current kubeconfig context's credentials "+
			"cannot produce a bearer token (client-certificate credentials); use a context with token or "+
			"exec-plugin credentials, set the Helm value security.authentication.mode to permissive, or have the "+
			"manager enable x509 client-certificate authentication (Helm value security.authentication.x509.enabled)",
		vi.GetName())
}

type versionAPI interface {
	Version(context.Context, *empty.Empty, ...grpc.CallOption) (*manager.VersionInfo2, error)
}

func (kc *Cluster) ConnectToAgent(
	dialCtx context.Context,
	namespace string,
	podName string,
	port uint16,
	podID types.UID,
) (*grpc.ClientConn, agent.AgentClient, *manager.VersionInfo2, error) {
	var grpcAddr string
	if podID == "" {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d", podName, namespace, port)
	} else {
		grpcAddr = fmt.Sprintf("pod/%s.%s:%d#%s", podName, namespace, port, podID)
	}
	conn, err := kc.dialGRPC(dialCtx, grpcAddr)
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

func (kc *Cluster) dialGRPC(dialCtx context.Context, address string, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	opts := append(make([]grpc.DialOption, 0, 5+len(extra)),
		grpc.WithContextDialer(portforward.Dialer(kc)),
		grpc.WithResolvers(portforward.NewResolver(kc)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 5 * time.Minute, Timeout: 20 * time.Second}),
		grpc.WithIdleTimeout(0),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	opts = append(opts, extra...)
	return grpcClient.DialGRPC(dialCtx, portforward.K8sPFScheme+":///"+address, opts...)
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
		clog.Infof(ctx, "Connected to %s %s", vi.Name, vi.Version)
	}
	return vi, err
}
