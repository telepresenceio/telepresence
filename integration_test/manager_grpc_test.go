package integration_test

import (
	"fmt"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

type managerGRPCSuite struct {
	itest.Suite
	itest.TrafficManager
	conn *grpc.ClientConn
	si   *manager.SessionInfo
}

func (m *managerGRPCSuite) SuiteName() string {
	return "ManagerGRPC"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &managerGRPCSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (m *managerGRPCSuite) SetupSuite() {
	m.Suite.SetupSuite()

	ctx := m.Context()
	ctx, k8sCluster, err := m.GetK8SCluster(ctx, "", m.ManagerNamespace())
	m.Require().NoError(err)

	ctx = portforward.WithRestConfig(ctx, k8sCluster.RestConfig)
	m.Require().NoError(err)
	m.conn, _, err = k8s.ConnectToManager(ctx, ctx, m.ManagerNamespace())
	m.Require().NoError(err)

	_, err = manager.NewManagerClient(m.conn).Version(m.Context(), &empty.Empty{})
	m.Require().NoError(err)

	daemonID := daemon.NewIdentifier("", k8sCluster.Context, m.AppNamespace(), false)
	m.si, err = trafficmgr.LoadSessionInfoFromUserCache(ctx, daemonID)
	m.Require().NoError(err)
}

func (m *managerGRPCSuite) TearDownSuite() {
	if m.conn != nil {
		go m.conn.Close()
		m.conn = nil
	}
}

func (m *managerGRPCSuite) Test_ClusterInfo() {
	istream, err := manager.NewManagerClient(m.conn).WatchClusterInfo(m.Context(), m.si)
	m.Require().NoError(err)
	info, err := istream.Recv()
	m.Require().NoError(err)
	// We can't really legislate for the IPs, but we can make sure they're there. The rest should be the default config values.
	m.Require().NotNil(info.ManagerPodIp)
	m.Require().Equal(int32(8081), info.ManagerPodPort)
	m.Require().NotNil(info.InjectorSvcIp)
	injectorSvcPort := int32(443)
	if m.ManagerIsVersion(">=2.24.0") {
		injectorSvcPort = 8443
	}
	m.Require().Equal(injectorSvcPort, info.InjectorSvcPort)
	m.Require().Equal(fmt.Sprintf("agent-injector.%s", m.ManagerNamespace()), info.InjectorSvcHost)
}
