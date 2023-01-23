package trafficmgr

import (
	"context"
	stduser "os/user"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"
	v1core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	mock_userd "github.com/telepresenceio/telepresence/v2/pkg/client/userd/mocks"
	mock_trafficmgr "github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr/mocks"
)

const (
	installID = "074469a9-bd46-42ea-b448-6a3df3a59609"
	clusterID = "8360b868-2ea2-417a-8b0c-5839bc1dfdbd"
)

//go:generate mockgen -package=mock_trafficmgr -destination=mocks/kubeinterface_mock.go k8s.io/client-go/kubernetes Interface, corev1.CoreV1Interface
//go:generate mockgen -package=mock_trafficmgr -destination=mocks/managerclient_mock.go github.com/telepresenceio/telepresence/rpc/v2/manager ManagerClient
//go:generate mockgen -package=mock_trafficmgr -destination=mocks/kubecorev1_mock.go k8s.io/client-go/kubernetes/typed/core/v1 CoreV1Interface,NamespaceInterface,ServiceInterface
type suiteSessionManager struct {
	suite.Suite

	ctrl *gomock.Controller

	sessionManager *SessionManager

	reporter    *mock_userd.MockReporter
	userService *mock_userd.MockService

	kubeConfigResolver       *mock_trafficmgr.MockKubeConfigResolver
	kubernetesInterface      *mock_trafficmgr.MockInterface
	coreV1                   *mock_trafficmgr.MockCoreV1Interface
	PortForwardDialerBuilder *mock_trafficmgr.MockPortForwardDialerBuilder
	managerConnector         *mock_trafficmgr.MockManagerConnector
	managerClient            *mock_trafficmgr.MockManagerClient
	userSessionCache         *mock_trafficmgr.MockUserSessionCache
	clusterBuilder           *mock_trafficmgr.MockClusterBuilder
	helmInstaller            *mock_trafficmgr.MockHelmInstaller

	daemonManager *mock_trafficmgr.MockDaemonManager

	stdUser *mock_trafficmgr.MockUser
	stdOS   *mock_trafficmgr.MockOS

	kubeconfig *client.Kubeconfig
}

func (s *suiteSessionManager) SetupTest() {
	s.kubeconfig = &client.Kubeconfig{
		KubeconfigExtension: client.KubeconfigExtension{
			Manager: &client.ManagerConfig{
				Namespace: "ambassador",
			},
		},
		Namespace:   "ambassador",
		Context:     "my-context",
		Server:      "https://my-context.cluster.bakerstreet.io",
		FlagMap:     nil,
		ConfigFlags: &genericclioptions.ConfigFlags{},
		RestConfig: &rest.Config{
			ContentConfig:   rest.ContentConfig{},
			Impersonate:     rest.ImpersonationConfig{},
			TLSClientConfig: rest.TLSClientConfig{},
		},
	}

	s.ctrl = gomock.NewController(s.T())

	s.reporter = mock_userd.NewMockReporter(s.ctrl)
	s.userService = mock_userd.NewMockService(s.ctrl)

	s.kubeConfigResolver = mock_trafficmgr.NewMockKubeConfigResolver(s.ctrl)
	s.kubernetesInterface = mock_trafficmgr.NewMockInterface(s.ctrl)
	s.coreV1 = mock_trafficmgr.NewMockCoreV1Interface(s.ctrl)

	s.PortForwardDialerBuilder = mock_trafficmgr.NewMockPortForwardDialerBuilder(s.ctrl)
	s.managerConnector = mock_trafficmgr.NewMockManagerConnector(s.ctrl)
	s.managerClient = mock_trafficmgr.NewMockManagerClient(s.ctrl)
	s.userSessionCache = mock_trafficmgr.NewMockUserSessionCache(s.ctrl)
	s.clusterBuilder = mock_trafficmgr.NewMockClusterBuilder(s.ctrl)
	s.helmInstaller = mock_trafficmgr.NewMockHelmInstaller(s.ctrl)

	s.daemonManager = mock_trafficmgr.NewMockDaemonManager(s.ctrl)

	s.stdUser = mock_trafficmgr.NewMockUser(s.ctrl)
	s.stdOS = mock_trafficmgr.NewMockOS(s.ctrl)

	s.sessionManager = &SessionManager{
		kcr: s.kubeConfigResolver,
		p:   s.PortForwardDialerBuilder,
		mc:  s.managerConnector,
		sc:  s.userSessionCache,
		dm:  s.daemonManager,
		cb:  s.clusterBuilder,
		hi:  s.helmInstaller,

		os:   s.stdOS,
		user: s.stdUser,
	}

	s.kubernetesInterface.EXPECT().CoreV1().Return(s.coreV1).AnyTimes()

	s.stdUser.EXPECT().Current().Return(&stduser.User{
		Uid:      "1000",
		Gid:      "1000",
		Username: "jdoe",
		Name:     "John doe",
		HomeDir:  "/home/jdoe",
	}, nil).AnyTimes()

	s.stdOS.EXPECT().Hostname().Return("localhost", nil).AnyTimes()
}

func (s *suiteSessionManager) AfterTest(suiteName, testName string) {
	s.ctrl.Finish()
}

func (s *suiteSessionManager) namespaceAPI() *mock_trafficmgr.MockNamespaceInterface {
	namespace := mock_trafficmgr.NewMockNamespaceInterface(s.ctrl)
	s.coreV1.EXPECT().Namespaces().Return(namespace).AnyTimes()
	return namespace
}

func (s *suiteSessionManager) servicesAPI(namespace string) *mock_trafficmgr.MockServiceInterface {
	services := mock_trafficmgr.NewMockServiceInterface(s.ctrl)
	s.coreV1.EXPECT().Services(namespace).Return(services).AnyTimes()
	return services
}

func (s *suiteSessionManager) contextWithEnv(ctx context.Context) context.Context {
	return client.WithEnv(ctx, &client.Env{})
}

func (s *suiteSessionManager) contextWithUserService(ctx context.Context) context.Context {
	return userd.WithService(ctx, s.userService)
}

func (s *suiteSessionManager) contextWithClientConfig(ctx context.Context) context.Context {
	return client.WithConfig(ctx, &client.Config{
		Timeouts: client.Timeouts{
			PrivateClusterConnect:        60 * time.Minute,
			PrivateTrafficManagerConnect: 60 * time.Minute,
		},
		LogLevels:       client.LogLevels{},
		Images:          client.Images{},
		Cloud:           client.Cloud{},
		Grpc:            client.Grpc{},
		TelepresenceAPI: client.TelepresenceAPI{},
		Intercept:       client.Intercept{},
	})
}

func (s *suiteSessionManager) buildContext() context.Context {
	ctx := context.Background()
	ctx = s.contextWithEnv(ctx)
	ctx = s.contextWithClientConfig(ctx)
	ctx = s.contextWithUserService(ctx)
	return k8sapi.WithK8sInterface(ctx, s.kubernetesInterface)
}

func (s *suiteSessionManager) TestFirstNewSessionLaptop() {
	// given
	initialCtx := s.buildContext()
	connectRequest := &rpc.ConnectRequest{
		KubeFlags: map[string]string{}, IsPodDaemon: false, ManagerNamespace: "another-ambassador",
	}

	// connectCluster
	cluster := &k8s.Cluster{Kubeconfig: s.kubeconfig, Ki: s.kubernetesInterface}
	s.reporter.EXPECT().Report(initialCtx, "connect")
	s.kubeConfigResolver.EXPECT().NewKubeconfig(initialCtx, connectRequest.KubeFlags, "another-ambassador").Return(s.kubeconfig, nil)
	s.clusterBuilder.EXPECT().NewCluster(initialCtx, s.kubeconfig, nil).Return(cluster, nil)
	ctxWithCluster := cluster.WithK8sInterface(initialCtx) // updated context with k8s cluster connection.

	// All calls to GetClusterId
	s.namespaceAPI().EXPECT().Get(ctxWithCluster, "default", v1.GetOptions{}).Return(&v1core.Namespace{
		ObjectMeta: v1.ObjectMeta{UID: clusterID},
	}, nil).Times(2)

	// Set metadata and report connection.
	s.reporter.EXPECT().SetMetadatum(ctxWithCluster, "cluster_id", clusterID)
	s.reporter.EXPECT().Report(ctxWithCluster, "connecting_traffic_manager", scout.Entry{
		Key:   "mapped_namespaces",
		Value: 0,
	})

	// connectMgr
	s.servicesAPI("ambassador").EXPECT().Get(gomock.Any(), "traffic-manager", meta.GetOptions{}).
		Return(nil, nil)
	dial := mock_trafficmgr.NewMockPortForwardDialer(s.ctrl)
	s.PortForwardDialerBuilder.EXPECT().
		NewK8sPortForwardDialer(gomock.Any(), cluster.Kubeconfig.RestConfig, k8sapi.GetK8sInterface(ctxWithCluster)).
		Return(dial, nil)
	s.managerConnector.EXPECT().Connect(gomock.Any(), cluster.GetManagerNamespace(), gomock.Any()).
		Return(&grpc.ClientConn{}, s.managerClient, &manager.VersionInfo2{
			Name: "", Version: "v3.10.4",
		}, nil)
	// Session info is not in cache, so connect then to get it, and set it in cache.
	s.userSessionCache.EXPECT().LoadSessionInfoFromUserCache(gomock.Any(), gomock.Any()).Return(nil, nil)
	newSessionInfo := &manager.SessionInfo{
		SessionId: "1234",
	}
	s.managerClient.EXPECT().
		ArriveAsClient(gomock.Any(), &manager.ClientInfo{
			Name:      "jdoe@localhost",
			InstallId: installID,
			Product:   "telepresence",
			Version:   client.Version(),
		}).Return(newSessionInfo, nil)
	s.userSessionCache.EXPECT().SaveSessionInfoToUserCache(gomock.Any(), gomock.Any(), newSessionInfo).Return(nil)
	// Set manager client into the user service
	s.userService.EXPECT().SetManagerClient(s.managerClient)
	s.reporter.EXPECT().InstallID().Return(installID)
	s.managerClient.EXPECT().GetClientConfig(gomock.Any(), &empty.Empty{}).Return(&manager.CLIConfig{}, nil)

	// Test if daemon is running, then build client.
	s.daemonManager.EXPECT().IsRunning(ctxWithCluster).Return(true, nil)
	daemonClient := mock_trafficmgr.NewMockDaemonClient(s.ctrl)
	daemonConn := mock_trafficmgr.NewMockCloser(s.ctrl)
	s.daemonManager.EXPECT().Client(ctxWithCluster).Return(daemonConn, daemonClient, nil)
	daemonClient.EXPECT().Connect(ctxWithCluster, gomock.Any()).Return(&daemon.DaemonStatus{
		OutboundConfig: &daemon.OutboundInfo{
			Session: &manager.SessionInfo{
				SessionId: "1234",
			},
		},
		Version: nil,
	}, nil)
	daemonClient.EXPECT().WaitForNetwork(gomock.Any(), &empty.Empty{}).Return(nil, nil)

	// Report the end of the traffic manager connection
	s.reporter.EXPECT().Report(ctxWithCluster, "finished_connecting_traffic_manager", gomock.Any())
	daemonClient.EXPECT().SetDnsSearchPath(ctxWithCluster, &daemon.Paths{
		Paths:      []string{},
		Namespaces: nil,
	})

	// when
	newCtx, sess, c := s.sessionManager.NewSession(initialCtx, s.reporter, connectRequest)

	// then
	assert.NotNil(s.T(), newCtx)
	assert.NotNil(s.T(), sess)
	assert.NotNil(s.T(), c)
}

func (s *suiteSessionManager) TestEnsureManagerForDaemonWithRequest() {
	// given
	ctx := s.buildContext()
	cluster := &k8s.Cluster{Kubeconfig: s.kubeconfig, Ki: s.kubernetesInterface}
	connectRequest := &rpc.ConnectRequest{KubeFlags: map[string]string{}, IsPodDaemon: true}
	helmRequest := &rpc.HelmRequest{
		ConnectRequest: connectRequest,
	}

	s.kubeConfigResolver.EXPECT().NewInClusterConfig(ctx, connectRequest.KubeFlags).
		Return(s.kubeconfig, nil)
	s.clusterBuilder.EXPECT().NewCluster(ctx, s.kubeconfig, nil).Return(cluster, nil)
	s.helmInstaller.EXPECT().
		EnsureTrafficManager(cluster.WithK8sInterface(ctx), cluster.ConfigFlags, "ambassador", helmRequest)

	// when
	err := s.sessionManager.EnsureManager(ctx, helmRequest)

	// then
	assert.NoError(s.T(), err)
}

func (s *suiteSessionManager) TestDeleteManagerWithDefault() {
	// given
	ctx := s.buildContext()
	cluster := &k8s.Cluster{Kubeconfig: s.kubeconfig, Ki: s.kubernetesInterface}
	defaultConnectionRequest := &rpc.ConnectRequest{
		ManagerNamespace: "",
	}

	s.kubeConfigResolver.EXPECT().NewKubeconfig(ctx, defaultConnectionRequest.KubeFlags, "").
		Return(s.kubeconfig, nil)
	s.clusterBuilder.EXPECT().NewCluster(ctx, s.kubeconfig, nil).Return(cluster, nil)
	s.helmInstaller.EXPECT().DeleteTrafficManager(ctx, cluster.ConfigFlags, "ambassador", false)

	// when
	err := s.sessionManager.DeleteManager(ctx, &rpc.HelmRequest{
		ConnectRequest: nil,
	})

	// then
	assert.NoError(s.T(), err)
}

func (s *suiteSessionManager) TestDeleteManagerWithRequest() {
	// given
	ctx := s.buildContext()
	connectRequest := &rpc.ConnectRequest{
		KubeFlags:        map[string]string{},
		IsPodDaemon:      false,
		ManagerNamespace: "ambassador",
	}
	cluster := &k8s.Cluster{Kubeconfig: s.kubeconfig, Ki: s.kubernetesInterface}

	s.kubeConfigResolver.EXPECT().NewKubeconfig(ctx, connectRequest.KubeFlags, "ambassador").Return(s.kubeconfig, nil)
	s.clusterBuilder.EXPECT().NewCluster(ctx, s.kubeconfig, nil).Return(cluster, nil)
	s.helmInstaller.EXPECT().DeleteTrafficManager(ctx, cluster.ConfigFlags, "ambassador", false)

	// when
	err := s.sessionManager.DeleteManager(ctx, &rpc.HelmRequest{
		ConnectRequest: connectRequest,
	})

	// then
	assert.NoError(s.T(), err)
}

func TestSuiteSessionManager(t *testing.T) {
	suite.Run(t, new(suiteSessionManager))
}
