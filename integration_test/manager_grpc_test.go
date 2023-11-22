package integration_test

import (
	"fmt"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

type managerGRPCSuite struct {
	itest.Suite
	itest.NamespacePair
	conn   *grpc.ClientConn
	client manager.ManagerClient
	si     *manager.SessionInfo
}

func (m *managerGRPCSuite) SuiteName() string {
	return "ManagerGRPC"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &managerGRPCSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (m *managerGRPCSuite) SetupSuite() {
	m.Suite.SetupSuite()

	ctx := m.Context()
	ctx, k8sCluster, err := m.GetK8SCluster(ctx, "", m.ManagerNamespace())
	m.Require().NoError(err)

	pfDialer, err := dnet.NewK8sPortForwardDialer(ctx, k8sCluster.RestConfig, k8sapi.GetK8sInterface(ctx))
	m.Require().NoError(err)
	m.conn, m.client, _, err = k8sclient.ConnectToManager(ctx, m.ManagerNamespace(), pfDialer.Dial)
	m.Require().NoError(err)

	_, err = m.client.Version(ctx, &empty.Empty{})
	m.Require().NoError(err)

	daemonID, err := daemon.NewIdentifier("", k8sCluster.Context, m.AppNamespace(), false)
	m.Require().NoError(err)
	m.si, err = trafficmgr.LoadSessionInfoFromUserCache(ctx, daemonID)
	m.Require().NoError(err)
}

func (m *managerGRPCSuite) TearDownSuite() {
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
		m.client = nil
	}
}

func (m *managerGRPCSuite) Test_ClusterInfo() {
	istream, err := m.client.WatchClusterInfo(m.Context(), m.si)
	m.Require().NoError(err)
	info, err := istream.Recv()
	m.Require().NoError(err)
	// We can't really legislate for the IPs, but we can make sure they're there. The rest should be the default config values.
	m.Require().NotNil(info.ManagerPodIp)
	m.Require().Equal(int32(8081), info.ManagerPodPort)
	m.Require().NotNil(info.InjectorSvcIp)
	m.Require().Equal(int32(443), info.InjectorSvcPort)
	m.Require().Equal(fmt.Sprintf("agent-injector.%s", m.ManagerNamespace()), info.InjectorSvcHost)
}
