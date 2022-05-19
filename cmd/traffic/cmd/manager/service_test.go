package manager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	fakeDiscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/test"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license/licensetest"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func dumps(o interface{}) string {
	bs, _ := json.Marshal(o)
	return string(bs)
}

func TestConnect(t *testing.T) {
	dlog.SetFallbackLogger(dlog.WrapTB(t, false))
	ctx := dlog.NewTestContext(t, true)
	a := assert.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version = "testing"

	conn := getTestClientConn(ctx, t)
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	ver, err := client.Version(ctx, &empty.Empty{})
	a.NoError(err)
	a.Equal(version.Version, ver.Version)

	// Alice arrives and departs

	aliceSess1, err := client.ArriveAsClient(ctx, testClients["alice"])
	a.NoError(err)
	t.Logf("aliceSess1: %v", aliceSess1)

	_, err = client.Depart(ctx, aliceSess1)
	a.NoError(err)

	// Alice arrives and sees no agents or intercepts

	aliceSess2, err := client.ArriveAsClient(ctx, testClients["alice"])
	a.NoError(err)
	t.Logf("aliceSess2: %v", aliceSess2)

	t.Log("WatchAgents(aliceSess2)...")
	aliceWA, err := client.WatchAgents(ctx, aliceSess2)
	a.NoError(err)

	aSnapA, err := aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 0)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	t.Log("WatchIntercepts(aliceSess2)...")
	aliceWI, err := client.WatchIntercepts(ctx, aliceSess2)
	a.NoError(err)

	aSnapI, err := aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 0)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	// Hello's agent arrives

	helloSess, err := client.ArriveAsAgent(ctx, testAgents["hello"])
	a.NoError(err)
	t.Logf("helloSess: %v", helloSess)

	t.Log("WatchIntercepts(helloSess)...")
	helloWI, err := client.WatchIntercepts(ctx, helloSess)
	a.NoError(err)

	hSnapI, err := helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 0)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Alice sees an agent

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 1)
	a.True(proto.Equal(testAgents["hello"], aSnapA.Agents[0]))
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Demo Deployment comes up with two Pods

	demo1Sess, err := client.ArriveAsAgent(ctx, testAgents["demo1"])
	a.NoError(err)
	t.Logf("demo1Sess: %v", demo1Sess)

	demo1WI, err := client.WatchIntercepts(ctx, demo1Sess)
	a.NoError(err)

	d1SnapI, err := demo1WI.Recv()
	a.NoError(err)
	a.Len(d1SnapI.Intercepts, 0)
	t.Logf("=> agent[demo1] interface snapshot = %s", dumps(d1SnapI))

	demo2Sess, err := client.ArriveAsAgent(ctx, testAgents["demo2"])
	a.NoError(err)
	t.Logf("demo2Sess: %v", demo2Sess)

	demo2WI, err := client.WatchIntercepts(ctx, demo2Sess)
	a.NoError(err)

	d2SnapI, err := demo2WI.Recv()
	a.NoError(err)
	a.Len(d2SnapI.Intercepts, 0)
	t.Logf("=> agent[demo2] interface snapshot = %s", dumps(d2SnapI))

	// Alice sees all the agents

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	if len(aSnapA.Agents) == 2 {
		t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))
		t.Logf("=> client[alice] trying again...")
		aSnapA, err = aliceWA.Recv()
		a.NoError(err)
	}
	a.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Alice remains

	_, err = client.Remain(ctx, &rpc.RemainRequest{Session: aliceSess2})
	a.NoError(err)

	// Hello Pro's agent arrives and departs

	helloProSess, err := client.ArriveAsAgent(ctx, testAgents["helloPro"])
	a.NoError(err)
	t.Logf("helloProSess: %v", helloProSess)

	helloProWI, err := client.WatchIntercepts(ctx, helloProSess)
	a.NoError(err)

	hPSnapI, err := helloProWI.Recv()
	a.NoError(err)
	a.Len(hPSnapI.Intercepts, 0)
	t.Logf("=> agent[helloPro] intercept snapshot = %s", dumps(hPSnapI))

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 4)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	_, err = client.Depart(ctx, helloProSess)
	a.NoError(err)

	aSnapA, err = aliceWA.Recv()
	a.NoError(err)
	a.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Alice creates an intercept

	spec := &rpc.InterceptSpec{
		Name:       "first",
		Namespace:  "default",
		Client:     testClients["alice"].Name,
		Agent:      testAgents["hello"].Name,
		Mechanism:  "tcp",
		TargetHost: "asdf",
		TargetPort: 9876,
	}

	first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
		Session:       aliceSess2,
		InterceptSpec: spec,
	})
	a.NoError(err)
	a.True(proto.Equal(spec, first.Spec))
	t.Logf("=> intercept info: %s", dumps(first))

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_WAITING, aSnapI.Intercepts[0].Disposition)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_WAITING, hSnapI.Intercepts[0].Disposition)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Hello's agent reviews the intercept

	_, err = client.ReviewIntercept(ctx, &rpc.ReviewInterceptRequest{
		Session:     helloSess,
		Id:          hSnapI.Intercepts[0].Id,
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Message:     "okay!",
	})
	a.NoError(err)

	// Causing the intercept to go active with a port assigned

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_ACTIVE, aSnapI.Intercepts[0].Disposition)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 1)
	a.Equal(rpc.InterceptDispositionType_ACTIVE, hSnapI.Intercepts[0].Disposition)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Creating a duplicate intercept yields an error

	second, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
		Session:       aliceSess2,
		InterceptSpec: spec,
	})
	a.Error(err)
	a.Nil(second)
	t.Logf("=> intercept info: %s", dumps(second))

	// Alice removes the intercept

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: aliceSess2,
		Name:    spec.Name,
	})
	a.NoError(err)
	t.Logf("removed intercept")

	aSnapI, err = aliceWI.Recv()
	a.NoError(err)
	a.Len(aSnapI.Intercepts, 0)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	hSnapI, err = helloWI.Recv()
	a.NoError(err)
	a.Len(hSnapI.Intercepts, 0)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Removing a bogus intercept yields an error

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: aliceSess2,
		Name:    spec.Name, // no longer present, right?
	})
	a.Error(err)

	_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
		Session: aliceSess1, // no longer a valid session, right?
		Name:    spec.Name,  // doesn't matter...
	})
	a.Error(err)
	_, err = client.Depart(ctx, aliceSess2)
	a.NoError(err)
	_, err = client.Depart(ctx, helloSess)
	a.NoError(err)
	_, err = client.Depart(ctx, demo1Sess)
	a.NoError(err)
	_, err = client.Depart(ctx, demo2Sess)
	a.NoError(err)
}

func TestArriveAsClient(t *testing.T) {
	prevVersion := version.Version
	defer func() { version.Version = prevVersion }()
	version.Version = "v2.6.0-testing"

	ctx := context.Background()
	conn := getTestClientConn(ctx, t)
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	ver, err := client.Version(ctx, &empty.Empty{})
	require.NoError(t, err)
	require.Equal(t, version.Version, ver.Version)

	sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
		Name:      "TEST_NAME",
		InstallId: "TEST_INSTALL_ID",
		Product:   "telepresence",
		Version:   "v2.6.0-testing",
	})
	require.NoError(t, err)
	require.NotNil(t, sess)
}

func TestArriveAsClient_ProprietaryAgent(t *testing.T) {
	type expectationFunc func(*testing.T, *rpc.SessionInfo, error)
	expectSucc := func(t *testing.T, sess *rpc.SessionInfo, err error) {
		require.NoError(t, err)
		require.NotNil(t, sess)
	}
	expectPermissionDenied := func(t *testing.T, sess *rpc.SessionInfo, err error) {
		require.Error(t, err)
		require.Nil(t, sess)
		statErr, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, statErr.Code(), codes.PermissionDenied)
	}

	prevVersion := version.Version
	defer func() { version.Version = prevVersion }()
	version.Version = "v2.6.0-testing"

	ctx := context.Background()
	env := managerutil.Env{
		AgentRegistry: "datawire.io",
		AgentImage:    "telepresence-traffic-agent",
	}
	ctx = managerutil.WithEnv(ctx, &env)

	t.Run("proprietary agent with no license and no clusterID", func(t *testing.T) {
		k8sClient := fake.NewSimpleClientset()
		ctx = k8sapi.WithK8sInterface(ctx, k8sClient)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()

		client := rpc.NewManagerClient(conn)
		sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
			Name:      "TEST_NAME",
			InstallId: "TEST_INSTALL_ID",
			Product:   "telepresence",
			Version:   "v2.6.0-testing",
		})
		expectPermissionDenied(t, sess, err)
		require.Contains(t, err.Error(), "license")
		require.Contains(t, err.Error(), "clusterID")
	})

	t.Run("proprietary agent with no license", func(t *testing.T) {
		k8sClient := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
				UID:  "TEST_CLUSTER_ID",
			},
		})
		ctx = k8sapi.WithK8sInterface(ctx, k8sClient)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()

		client := rpc.NewManagerClient(conn)
		// 2 unauthed clients connect successfully, third should get permission error
		expectations := []expectationFunc{
			expectSucc, expectSucc, expectPermissionDenied,
		}
		for idx, expectation := range expectations {
			sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
				Name:      fmt.Sprintf("TEST_NAME_%d", idx),
				InstallId: fmt.Sprintf("TEST_INSTALL_ID_%d", idx),
				Version:   version.Version,
				Product:   "telepresence",
			})
			expectation(t, sess, err)
		}

		// authed clients should be able to connect successfully, checking 3
		for idx := 0; idx < 2; idx++ {
			sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
				Name:      fmt.Sprintf("TEST_NAME_%d", idx),
				InstallId: fmt.Sprintf("TEST_INSTALL_ID_%d", idx),
				Version:   version.Version,
				Product:   "telepresence",
				ApiKey:    fmt.Sprintf("TEST_API_KEY_%d", idx),
			})
			expectSucc(t, sess, err)
		}
	})

	t.Run("proprietary agent with license", func(t *testing.T) {
		clusterID := "TEST_CLUSTER_ID"
		k8sClient := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
				UID:  types.UID(clusterID),
			},
		})
		ctx = k8sapi.WithK8sInterface(ctx, k8sClient)

		host := "test-auth.datawire.io"
		privKey, pubKey, err := licensetest.GenKeys()
		require.NoError(t, err)
		licenseString, err := licensetest.GenLicense(privKey, clusterID, map[string]interface{}{
			"maxClientCount": 2,
		})
		require.NoError(t, err)

		ctx = license.WithBundle(ctx, licenseString, host, map[string]string{
			host: pubKey,
		})

		conn := getTestClientConn(ctx, t)
		defer conn.Close()

		client := rpc.NewManagerClient(conn)
		// 2 unauthed clients connect successfully, third should get permission error
		expectations := []expectationFunc{
			expectSucc, expectSucc, expectPermissionDenied,
		}
		for idx, expectation := range expectations {
			sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
				Name:      fmt.Sprintf("TEST_NAME_%d", idx),
				InstallId: fmt.Sprintf("TEST_INSTALL_ID_%d", idx),
				Version:   version.Version,
				Product:   "telepresence",
			})
			expectation(t, sess, err)
		}

		// 2 authed clients connect successfully, third should get permission error
		expectations = []expectationFunc{
			expectSucc, expectSucc, expectPermissionDenied,
		}
		for idx, expectation := range expectations {
			sess, err := client.ArriveAsClient(ctx, &rpc.ClientInfo{
				Name:      fmt.Sprintf("TEST_NAME_%d", idx),
				InstallId: fmt.Sprintf("TEST_INSTALL_ID_%d", idx),
				Version:   version.Version,
				Product:   "telepresence",
				ApiKey:    fmt.Sprintf("TEST_API_KEY_%d", idx),
			})
			expectation(t, sess, err)
		}
	})
}

func getTestClientConn(ctx context.Context, t *testing.T) *grpc.ClientConn {
	const bufsize = 64 * 1024
	ctx, cancel := context.WithCancel(ctx)

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	// ensure fake k8s client is configured correctly, creating a new one if one was not already in the context
	fakeClient := k8sapi.GetK8sInterface(ctx)
	if fakeClient == nil {
		fakeClient = fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
				UID:  "",
			},
		})
		ctx = k8sapi.WithK8sInterface(ctx, fakeClient)
	}
	fakeClient.Discovery().(*fakeDiscovery.FakeDiscovery).FakedServerVersion = &k8sVersion.Info{
		GitVersion: "v1.17.0",
	}

	// ensure env is configured correctly, creating a new one if one was not already in the context
	env := managerutil.GetEnv(ctx)
	if env == nil {
		env = &managerutil.Env{}
		ctx = managerutil.WithEnv(ctx, env)
	}
	env.MaxReceiveSize = resource.Quantity{}
	env.PodCIDRStrategy = "environment"
	env.PodCIDRs = "192.168.0.0/16"

	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}

	s := grpc.NewServer()
	mgr, ctx := manager.NewManager(ctx)
	rpc.RegisterManagerServer(s, mgr)

	errCh := make(chan error)
	go func() {
		sc := &dhttp.ServerConfig{
			Handler: s,
		}
		errCh <- sc.Serve(ctx, lis)
		close(errCh)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil && err != ctx.Err() {
			t.Error(err)
		}
	})

	return conn
}
