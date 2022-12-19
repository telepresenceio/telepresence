package manager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	fakeDiscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/test"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	mockmanagerutil "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil/mocks"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func dumps(o any) string {
	bs, _ := json.Marshal(o)
	return string(bs)
}

func TestConnect(t *testing.T) {
	dlog.SetFallbackLogger(dlog.WrapTB(t, false))
	ctx := dlog.NewTestContext(t, true)
	a := assert.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

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

func TestRemoveIntercept_InterceptFinalizer(t *testing.T) {
	var (
		testClients = testdata.GetTestClients(t)
		testAgents  = testdata.GetTestAgents(t)
		spec        = &rpc.InterceptSpec{
			Name:       "first",
			Namespace:  "default",
			Client:     testClients["alice"].Name,
			Agent:      testAgents["hello"].Name,
			Mechanism:  "tcp",
			TargetHost: "asdf",
			TargetPort: 9876,
		}
		a8rCloudConfig = rpc.AmbassadorCloudConfig{
			Host: "hostname",
			Port: "8080",
		}
	)

	prevVersion := version.Version
	defer func() { version.Version = prevVersion }()
	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

	t.Run("error removing intercept with systema", func(t *testing.T) {
		dlog.SetFallbackLogger(dlog.WrapTB(t, false))
		ctx := dlog.NewTestContext(t, false)
		a := assert.New(t)

		mockSysaCRUDClient := mockmanagerutil.NewMockSystemaCRUDClient(gomock.NewController(t))
		mockSysaCRUDClient.EXPECT().
			RemoveIntercept(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("ERROR"))

		mockSysa := mockA8rCloudClientProvider[managerutil.SystemaCRUDClient]{
			t:            t,
			expectations: map[string][]*mockExpectation{},
		}
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &mockSysa)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		a.NoError(err)

		first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			Session:       sess,
			InterceptSpec: spec,
			ApiKey:        "apiKey",
		})
		a.NoError(err)
		a.True(proto.Equal(spec, first.Spec))

		_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
			Name:    spec.Name,
			Session: sess,
		})
		a.NoError(err)
	})

	t.Run("error closing connection with systema", func(t *testing.T) {
		dlog.SetFallbackLogger(dlog.WrapTB(t, false))
		ctx := dlog.NewTestContext(t, false)
		a := assert.New(t)

		mockSysaCRUDClient := mockmanagerutil.NewMockSystemaCRUDClient(gomock.NewController(t))
		mockSysaCRUDClient.EXPECT().
			RemoveIntercept(gomock.Any(), gomock.Any()).
			Return(&empty.Empty{}, nil)
		mockSysaCRUDClient.EXPECT().
			Close(gomock.Any()).
			Return(errors.New("ERROR"))

		mockSysa := mockA8rCloudClientProvider[managerutil.SystemaCRUDClient]{
			t:            t,
			expectations: map[string][]*mockExpectation{},
		}
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &mockSysa)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		a.NoError(err)

		first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			Session:       sess,
			InterceptSpec: spec,
			ApiKey:        "apiKey",
		})

		a.NoError(err)
		a.True(proto.Equal(spec, first.Spec))

		_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
			Name:    spec.Name,
			Session: sess,
		})
		a.NoError(err)
	})

	t.Run("no error removing intercept with systema", func(t *testing.T) {
		dlog.SetFallbackLogger(dlog.WrapTB(t, false))
		ctx := dlog.NewTestContext(t, false)
		a := assert.New(t)

		mockSysaCRUDClient := mockmanagerutil.NewMockSystemaCRUDClient(gomock.NewController(t))
		mockSysaCRUDClient.EXPECT().
			RemoveIntercept(gomock.Any(), gomock.Any()).
			Return(&empty.Empty{}, nil)
		mockSysaCRUDClient.EXPECT().
			Close(gomock.Any()).
			Return(nil)

		mockSysa := mockA8rCloudClientProvider[managerutil.SystemaCRUDClient]{
			t:            t,
			expectations: map[string][]*mockExpectation{},
		}
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &mockSysa)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		a.NoError(err)

		first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			Session:       sess,
			InterceptSpec: spec,
			ApiKey:        "apiKey",
		})

		a.NoError(err)
		a.True(proto.Equal(spec, first.Spec))

		_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
			Name:    spec.Name,
			Session: sess,
		})
		a.NoError(err)
	})
}

func TestUpdateIntercept(t *testing.T) {
	var (
		testClients = testdata.GetTestClients(t)
		testAgents  = testdata.GetTestAgents(t)
		spec        = &rpc.InterceptSpec{
			Name:       "first",
			Namespace:  "default",
			Client:     testClients["alice"].Name,
			Agent:      testAgents["hello"].Name,
			Mechanism:  "tcp",
			TargetHost: "asdf",
			TargetPort: 9876,
		}
		a8rCloudConfig = rpc.AmbassadorCloudConfig{
			Host: "hostname",
			Port: "8080",
		}
	)

	prevVersion := version.Version
	defer func() { version.Version = prevVersion }()
	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

	t.Run("add preview domain", func(t *testing.T) {
		dlog.SetFallbackLogger(dlog.WrapTB(t, false))
		ctx := dlog.NewTestContext(t, false)
		a := assert.New(t)

		mockSysaCRUDClient := mockmanagerutil.NewMockSystemaCRUDClient(gomock.NewController(t))
		mockSysaCRUDClient.EXPECT().
			CreateDomain(gomock.Any(), gomock.Any()).
			Return(&systema.CreateDomainResponse{
				Domain: "test.com",
			}, nil)
		mockSysaCRUDClient.EXPECT().
			RemoveDomain(gomock.Any(), gomock.Any()).
			Return(&empty.Empty{}, nil)
		mockSysaCRUDClient.EXPECT().
			Close(gomock.Any()).
			Return(nil)
		mockSysaCRUDClient.EXPECT().
			RemoveIntercept(gomock.Any(), gomock.Any()).
			Return(&empty.Empty{}, nil)
		mockSysaCRUDClient.EXPECT().
			Close(gomock.Any()).
			Return(nil)

		mockSysa := mockA8rCloudClientProvider[managerutil.SystemaCRUDClient]{
			t:            t,
			expectations: map[string][]*mockExpectation{},
		}
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &mockSysa)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		a.NoError(err)

		first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			Session:       sess,
			InterceptSpec: spec,
			ApiKey:        "apiKey",
		})

		a.NoError(err)
		a.True(proto.Equal(spec, first.Spec))

		_, err = client.UpdateIntercept(ctx, &rpc.UpdateInterceptRequest{
			Session: sess,
			Name:    spec.Name,
			PreviewDomainAction: &rpc.UpdateInterceptRequest_AddPreviewDomain{
				AddPreviewDomain: &rpc.PreviewSpec{
					Ingress: &rpc.IngressInfo{},
				},
			},
		})
		a.NoError(err)

		_, err = client.RemoveIntercept(ctx, &rpc.RemoveInterceptRequest2{
			Name:    spec.Name,
			Session: sess,
		})
		a.NoError(err)
	})

	t.Run("remove preview domain", func(t *testing.T) {
		dlog.SetFallbackLogger(dlog.WrapTB(t, false))
		ctx := dlog.NewTestContext(t, false)
		a := assert.New(t)

		mockSysaCRUDClient := mockmanagerutil.NewMockSystemaCRUDClient(gomock.NewController(t))
		mockSysaCRUDClient.EXPECT().
			CreateDomain(gomock.Any(), gomock.Any()).
			Return(&systema.CreateDomainResponse{
				Domain: "test.com",
			}, nil)
		mockSysaCRUDClient.EXPECT().
			RemoveDomain(gomock.Any(), gomock.Any()).
			Return(&empty.Empty{}, nil)

		mockSysa := mockA8rCloudClientProvider[managerutil.SystemaCRUDClient]{
			t:            t,
			expectations: map[string][]*mockExpectation{},
		}
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		mockSysa.EXPECT().
			GetCloudConfig(gomock.Any()).
			Return(&a8rCloudConfig, nil)
		mockSysa.EXPECT().
			BuildClient(gomock.Any(), gomock.Any()).
			Return(mockSysaCRUDClient, nil)
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &mockSysa)

		conn := getTestClientConn(ctx, t)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		a.NoError(err)

		first, err := client.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			Session:       sess,
			InterceptSpec: spec,
			ApiKey:        "apiKey",
		})

		a.NoError(err)
		a.True(proto.Equal(spec, first.Spec))

		_, err = client.UpdateIntercept(ctx, &rpc.UpdateInterceptRequest{
			Session: sess,
			Name:    spec.Name,
			PreviewDomainAction: &rpc.UpdateInterceptRequest_AddPreviewDomain{
				AddPreviewDomain: &rpc.PreviewSpec{
					Ingress: &rpc.IngressInfo{},
				},
			},
		})
		a.NoError(err)

		_, err = client.UpdateIntercept(ctx, &rpc.UpdateInterceptRequest{
			Session: sess,
			Name:    spec.Name,
			PreviewDomainAction: &rpc.UpdateInterceptRequest_RemovePreviewDomain{
				RemovePreviewDomain: true,
			},
		})
		a.NoError(err)
	})
}

func getTestClientConn(ctx context.Context, t *testing.T) *grpc.ClientConn {
	const bufsize = 64 * 1024
	var cancel func()
	ctx, cancel = context.WithCancel(ctx)

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	fakeClient := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	})
	fakeClient.Discovery().(*fakeDiscovery.FakeDiscovery).FakedServerVersion = &k8sVersion.Info{
		GitVersion: "v1.17.0",
	}
	ctx = k8sapi.WithK8sInterface(ctx, fakeClient)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{
		SystemAHost:     "localhost",
		SystemAPort:     1234,
		MaxReceiveSize:  resource.Quantity{},
		PodCIDRStrategy: "environment",
		PodCIDRs: []*net.IPNet{{
			IP:   net.IP{192, 168, 0, 0},
			Mask: net.CIDRMask(16, 32),
		}},
	})

	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}

	s := grpc.NewServer()
	mgr, ctx, err := manager.NewService(ctx)
	if err != nil {
		t.Fatalf("failed to build manager: %v", err)
	}
	mgr.RegisterServers(s)

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

type mockA8rCloudClientProvider[T a8rcloud.Closeable] struct {
	t            *testing.T
	expectations map[string][]*mockExpectation
}

func (m *mockA8rCloudClientProvider[T]) EXPECT() *mockA8rCloudClientProviderExpectationHandler[T] {
	return &mockA8rCloudClientProviderExpectationHandler[T]{m._addExpectation}
}

func (m *mockA8rCloudClientProvider[T]) _addExpectation(e *mockExpectation) {
	if m.expectations == nil {
		m.expectations = make(map[string][]*mockExpectation)
	}
	var (
		name         = e.name
		expectations = m.expectations[name]
	)
	if expectations == nil {
		expectations = make([]*mockExpectation, 0)
	}
	expectations = append(expectations, e)
	m.expectations[name] = expectations
}

func (m *mockA8rCloudClientProvider[T]) GetAPIKey(ctx context.Context) (string, error) {
	expectations := m.expectations["GetAPIKey"]
	if len(expectations) == 0 {
		err := errors.New("unexpected call to GetAPIKey")
		m.t.Error(err)
		return "", err
	}

	var e *mockExpectation
	e, expectations = expectations[0], expectations[1:]
	m.expectations["GetAPIKey"] = expectations

	arg := e.args[0]
	if matcher, ok := arg.(gomock.Matcher); ok {
		if !matcher.Matches(ctx) {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return "", err
		}
	} else {
		if cctx := arg.(context.Context); ctx != cctx {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return "", err
		}
	}

	ret := e.ret
	return ret[0].(string), castErr(ret[1])
}

func (m *mockA8rCloudClientProvider[T]) GetInstallID(ctx context.Context) (string, error) {
	expectations := m.expectations["GetInstallID"]
	if len(expectations) == 0 {
		err := errors.New("unexpected call to GetInstallID")
		m.t.Error(err)
		return "", err
	}

	var e *mockExpectation
	e, expectations = expectations[0], expectations[1:]
	m.expectations["GetInstallID"] = expectations

	arg := e.args[0]
	if matcher, ok := arg.(gomock.Matcher); ok {
		if !matcher.Matches(ctx) {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return "", err
		}
	} else {
		if cctx := arg.(context.Context); ctx != cctx {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return "", err
		}
	}

	ret := e.ret
	return ret[0].(string), castErr(ret[1])
}

func (m *mockA8rCloudClientProvider[T]) GetExtraHeaders(ctx context.Context) (map[string]string, error) {
	expectations := m.expectations["GetExtraHeaders"]
	if len(expectations) == 0 {
		err := errors.New("unexpected call to GetExtraHeaders")
		m.t.Error(err)
		return nil, err
	}

	var e *mockExpectation
	e, expectations = expectations[0], expectations[1:]
	m.expectations["GetExtraHeaders"] = expectations

	arg := e.args[0]
	if matcher, ok := arg.(gomock.Matcher); ok {
		if !matcher.Matches(ctx) {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	} else {
		if cctx := arg.(context.Context); ctx != cctx {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	}

	ret := e.ret
	return ret[0].(map[string]string), castErr(ret[1])
}

func (m *mockA8rCloudClientProvider[T]) GetCloudConfig(ctx context.Context) (*rpc.AmbassadorCloudConfig, error) {
	expectations := m.expectations["GetCloudConfig"]
	if len(expectations) == 0 {
		err := errors.New("unexpected call to GetCloudConfig")
		m.t.Error(err)
		return nil, err
	}

	var e *mockExpectation
	e, expectations = expectations[0], expectations[1:]
	m.expectations["GetCloudConfig"] = expectations

	arg := e.args[0]
	if matcher, ok := arg.(gomock.Matcher); ok {
		if !matcher.Matches(ctx) {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	} else {
		if cctx := arg.(context.Context); ctx != cctx {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	}

	ret := e.ret
	return ret[0].(*rpc.AmbassadorCloudConfig), castErr(ret[1])
}

func (m *mockA8rCloudClientProvider[T]) BuildClient(ctx context.Context, conn *grpc.ClientConn) (managerutil.SystemaCRUDClient, error) {
	expectations := m.expectations["BuildClient"]
	if len(expectations) == 0 {
		err := errors.New("unexpected call to BuildClient")
		m.t.Error(err)
		return nil, err
	}

	var e *mockExpectation
	e, expectations = expectations[0], expectations[1:]
	m.expectations["BuildClient"] = expectations

	arg := e.args[0]
	if matcher, ok := arg.(gomock.Matcher); ok {
		if !matcher.Matches(ctx) {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	} else {
		if cctx := arg.(context.Context); ctx != cctx {
			err := fmt.Errorf("invalid argument: %v", ctx)
			m.t.Error(err)
			return nil, err
		}
	}

	ret := e.ret
	return ret[0].(managerutil.SystemaCRUDClient), castErr(ret[1])
}

type mockA8rCloudClientProviderExpectationHandler[T a8rcloud.Closeable] struct {
	addExpectation func(*mockExpectation)
}

func (m *mockA8rCloudClientProviderExpectationHandler[T]) GetAPIKey(args ...any) *mockExpectation {
	e := mockExpectation{
		name: "GetAPIKey",
		args: args,
	}
	m.addExpectation(&e)
	return &e
}

func (m *mockA8rCloudClientProviderExpectationHandler[T]) GetInstallID(args ...any) *mockExpectation {
	e := mockExpectation{
		name: "GetInstallID",
		args: args,
	}
	m.addExpectation(&e)
	return &e
}

func (m *mockA8rCloudClientProviderExpectationHandler[T]) GetExtraHeaders(args ...any) *mockExpectation {
	e := mockExpectation{
		name: "GetExtraHeaders",
		args: args,
	}
	m.addExpectation(&e)
	return &e
}

func (m *mockA8rCloudClientProviderExpectationHandler[T]) GetCloudConfig(args ...any) *mockExpectation {
	e := mockExpectation{
		name: "GetCloudConfig",
		args: args,
	}
	m.addExpectation(&e)
	return &e
}

func (m *mockA8rCloudClientProviderExpectationHandler[T]) BuildClient(args ...any) *mockExpectation {
	e := mockExpectation{
		name: "BuildClient",
		args: args,
	}
	m.addExpectation(&e)
	return &e
}

type mockExpectation struct {
	name string
	args []any
	ret  []any
}

func (m *mockExpectation) Return(v ...any) {
	m.ret = v
}

func castErr(v any) error {
	if v == nil {
		return nil
	}

	return v.(error)
}
