package itest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type TrafficManager interface {
	NamespacePair
	DoWithTrafficManager(context.Context, func(context.Context, context.CancelFunc, manager.ManagerClient, *manager.SessionInfo)) error
	DoWithSession(context.Context, *rpc.ConnectRequest, func(context.Context, rpc.ConnectorServer)) error
	NewConnectRequest(context.Context) *rpc.ConnectRequest
}

type trafficManager struct {
	NamespacePair
}

func WithTrafficManager(np NamespacePair, f func(ctx context.Context, ch TrafficManager)) {
	np.HarnessT().Run("Test_TrafficManager", func(t *testing.T) {
		ctx := WithT(np.HarnessContext(), t)
		require.NoError(t, np.GeneralError())
		th := &trafficManager{NamespacePair: np}
		th.PushHarness(ctx, th.setup, th.tearDown)
		defer th.PopHarness()
		f(ctx, th)
	})
}

func (th *trafficManager) setup(ctx context.Context) bool {
	t := getT(ctx)
	TelepresenceQuitOk(ctx)
	_, err := th.TelepresenceHelmInstall(ctx, false)
	return assert.NoError(t, err)
}

func (th *trafficManager) tearDown(ctx context.Context) {
	th.UninstallTrafficManager(ctx, th.ManagerNamespace())
}

func (th *trafficManager) trafficManagerConnection(ctx context.Context) (*grpc.ClientConn, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", KubeConfig(ctx))
	if err != nil {
		return nil, err
	}
	return dialTrafficManager(ctx, cfg, th.ManagerNamespace())
}

func dialTrafficManager(ctx context.Context, cfg *rest.Config, managerNamespace string) (*grpc.ClientConn, error) {
	k8sApi, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	argoRollouApi, err := argorollouts.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	ctx = k8sapi.WithJoinedClientSetInterface(ctx, k8sApi, argoRollouApi)
	ctx = portforward.WithRestConfig(ctx, cfg)
	return grpc.NewClient(fmt.Sprintf(portforward.K8sPFScheme+":///svc/traffic-manager.%s:8081", managerNamespace),
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

// DoWithTrafficManager is intended to be used when testing the traffic-manager grpc. It simulates a connector client
// that has a session established with the traffic-manager.
func (th *trafficManager) DoWithTrafficManager(ctx context.Context, f func(context.Context, context.CancelFunc, manager.ManagerClient, *manager.SessionInfo)) error {
	conn, err := th.trafficManagerConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	mgr := manager.NewManagerClient(conn)

	// Retrieve the session info from the traffic-manager. This is how
	// a connection to a namespace is made. The traffic-manager now
	// associates the returned session with that namespace in subsequent
	// calls.
	clientSession, err := mgr.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "telepresence@datawire.io",
		Namespace: th.AppNamespace(),
		InstallId: "xxx",
		Product:   "telepresence",
		Version:   th.ClientVersion().String(),
	})
	if err != nil {
		return err
	}

	// Normal ticker routine to keep the client alive.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = mgr.Remain(ctx, &manager.RemainRequest{Session: clientSession})
			case <-ctx.Done():
				_, _ = mgr.Depart(ctx, clientSession)
				return
			}
		}
	}()
	f(ctx, cancel, mgr, clientSession)
	return nil
}

// NewConnectRequest returns a connector.ConnectRequest that has been initialized with default values. It's intended
// to be used in together with DoWithSession to provide an ability to create and modify the request used when
// connecting.
func (th *trafficManager) NewConnectRequest(ctx context.Context) *rpc.ConnectRequest {
	flags := map[string]string{
		"kubeconfig": KubeConfig(ctx),
		"namespace":  th.AppNamespace(),
	}
	if user := GetUser(ctx); user != "default" {
		flags["as"] = "system:serviceaccount:" + user
	}
	return &rpc.ConnectRequest{
		KubeFlags:        flags,
		MappedNamespaces: []string{th.AppNamespace()},
		ManagerNamespace: th.ManagerNamespace(),
		Environment:      th.GlobalEnv(ctx),
	}
}

// DoWithSession is intended to be used when testing the connector GRPC directly without using the CLI. A "connect" is
// made before calling the provided function, which means that the `rpc.ConnectorServer` can be used as a connected daemon. A
// call to quit is guaranteed after the function ends.
func (th *trafficManager) DoWithSession(ctx context.Context, cr *rpc.ConnectRequest, f func(context.Context, rpc.ConnectorServer)) error {
	client.ProcessName = func() string {
		return userd.ProcessName
	}
	ctx = cli.InitContext(ctx)
	ctx, err := logging.InitContext(ctx, "connector", logging.RotateNever, true, true)
	if err != nil {
		return err
	}

	ctx = dos.WithExe(ctx, th)
	err = connect.EnsureRootDaemonRunning(ctx)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	srv, err := userd.GetNewServiceFunc(ctx)(ctx, cancel, g, client.GetConfig(ctx), grpc.NewServer())
	if err != nil {
		return err
	}
	g.Go("connector", srv.ManageSessions)

	var sv rpc.ConnectorServer
	srv.As(&sv)

	cfg := client.GetConfig(ctx)
	if cfg.Intercept().UseFtp {
		g.Go("fuseftp-server", func(ctx context.Context) error {
			if err := srv.InitFTPServer(ctx); err != nil {
				dlog.Error(ctx, err)
			}
			<-ctx.Done()
			return nil
		})
	}

	var rsp *rpc.ConnectInfo
	rsp, err = sv.Connect(ctx, cr)
	if err != nil {
		return err
	}
	if rsp.Error != rpc.ConnectInfo_UNSPECIFIED && rsp.Error != rpc.ConnectInfo_ALREADY_CONNECTED {
		return errcat.Category(rsp.ErrorCategory).New(rsp.ErrorText)
	}
	func() {
		defer func() {
			_, _ = sv.Quit(ctx, nil)
		}()
		f(ctx, sv)
	}()
	return g.Wait()
}
