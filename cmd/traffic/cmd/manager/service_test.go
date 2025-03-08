package manager

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/go-json-experiment/json"
	"github.com/stretchr/testify/require"
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

	fakeargorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func dumps(o any) string {
	bs, _ := json.Marshal(o)
	return string(bs)
}

func TestConnect(t *testing.T) {
	dlog.SetFallbackLogger(dlog.WrapTB(t, false))
	ctx := dlog.NewTestContext(t, true)
	require := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

	conn := getTestClientConn(ctx, t)
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	ver, err := client.Version(ctx, &empty.Empty{})
	require.NoError(err)
	require.Equal(version.Version, ver.Version)

	// Alice arrives and departs

	aliceSess1, err := client.ArriveAsClient(ctx, testClients["alice"])
	require.NoError(err)
	t.Logf("aliceSess1: %v", aliceSess1)

	_, err = client.Depart(ctx, aliceSess1)
	require.NoError(err)

	// Alice arrives and sees no agents or intercepts

	aliceSess2, err := client.ArriveAsClient(ctx, testClients["alice"])
	require.NoError(err)
	t.Logf("aliceSess2: %v", aliceSess2)

	t.Log("WatchAgents(aliceSess2)...")
	aliceWA, err := client.WatchAgents(ctx, aliceSess2)
	require.NoError(err)

	aSnapA, err := aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 0)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	t.Log("WatchIntercepts(aliceSess2)...")
	aliceWI, err := client.WatchIntercepts(ctx, aliceSess2)
	require.NoError(err)

	aSnapI, err := aliceWI.Recv()
	require.NoError(err)
	require.Len(aSnapI.Intercepts, 0)
	t.Logf("=> client[alice] intercept snapshot = %s", dumps(aSnapI))

	// Hello's agent arrives

	helloSess, err := client.ArriveAsAgent(ctx, testAgents["hello"])
	require.NoError(err)
	t.Logf("helloSess: %v", helloSess)

	t.Log("WatchIntercepts(helloSess)...")
	helloWI, err := client.WatchIntercepts(ctx, helloSess)
	require.NoError(err)

	hSnapI, err := helloWI.Recv()
	require.NoError(err)
	require.Len(hSnapI.Intercepts, 0)
	t.Logf("=> agent[hello] intercept snapshot = %s", dumps(hSnapI))

	// Alice sees an agent

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 1)
	require.True(proto.Equal(testAgents["hello"], aSnapA.Agents[0]))
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Demo Deployment comes up with two Pods

	demo1Sess, err := client.ArriveAsAgent(ctx, testAgents["demo1"])
	require.NoError(err)
	t.Logf("demo1Sess: %v", demo1Sess)

	demo1WI, err := client.WatchIntercepts(ctx, demo1Sess)
	require.NoError(err)

	d1SnapI, err := demo1WI.Recv()
	require.NoError(err)
	require.Len(d1SnapI.Intercepts, 0)
	t.Logf("=> agent[demo1] interface snapshot = %s", dumps(d1SnapI))

	demo2Sess, err := client.ArriveAsAgent(ctx, testAgents["demo2"])
	require.NoError(err)
	t.Logf("demo2Sess: %v", demo2Sess)

	demo2WI, err := client.WatchIntercepts(ctx, demo2Sess)
	require.NoError(err)

	d2SnapI, err := demo2WI.Recv()
	require.NoError(err)
	require.Len(d2SnapI.Intercepts, 0)
	t.Logf("=> agent[demo2] interface snapshot = %s", dumps(d2SnapI))

	// Alice sees all the agents

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	if len(aSnapA.Agents) == 2 {
		t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))
		t.Logf("=> client[alice] trying again...")
		aSnapA, err = aliceWA.Recv()
		require.NoError(err)
	}
	require.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	// Alice remains

	_, err = client.Remain(ctx, &rpc.RemainRequest{Session: aliceSess2})
	require.NoError(err)

	// Hello Pro's agent arrives and departs

	helloProSess, err := client.ArriveAsAgent(ctx, testAgents["helloPro"])
	require.NoError(err)
	t.Logf("helloProSess: %v", helloProSess)

	helloProWI, err := client.WatchIntercepts(ctx, helloProSess)
	require.NoError(err)

	hPSnapI, err := helloProWI.Recv()
	require.NoError(err)
	require.Len(hPSnapI.Intercepts, 0)
	t.Logf("=> agent[helloPro] intercept snapshot = %s", dumps(hPSnapI))

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 4)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))

	_, err = client.Depart(ctx, helloProSess)
	require.NoError(err)

	aSnapA, err = aliceWA.Recv()
	require.NoError(err)
	require.Len(aSnapA.Agents, 3)
	t.Logf("=> client[alice] agent snapshot = %s", dumps(aSnapA))
	_, err = client.Depart(ctx, aliceSess2)
	require.NoError(err)
	_, err = client.Depart(ctx, helloSess)
	require.NoError(err)
	_, err = client.Depart(ctx, demo1Sess)
	require.NoError(err)
	_, err = client.Depart(ctx, demo2Sess)
	require.NoError(err)
}

func getTestClientConn(ctx context.Context, t *testing.T) *grpc.ClientConn {
	const bufsize = 64 * 1024
	var cancel func()
	ctx, cancel = context.WithCancel(ctx)

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	fakeClient := fake.NewClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
			Labels: map[string]string{
				labels.NameLabelKey: "default",
			},
		},
	})
	fakeClient.Discovery().(*fakeDiscovery.FakeDiscovery).FakedServerVersion = &k8sVersion.Info{
		GitVersion: "v1.30.5",
	}

	const mgrNs = "ambassador"
	_, err := fakeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: mgrNs,
			Labels: map[string]string{
				labels.NameLabelKey: mgrNs,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = fakeClient.CoreV1().ConfigMaps(mgrNs).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentconfig.ManagerAppName,
			Namespace: mgrNs,
		},
		Data: map[string]string{"namespace-selector.yaml": ` 
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: In
  values:
    - default
`},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")

	configWatcher := config.NewWatcher(mgrNs)
	go func() {
		if err := configWatcher.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err = configWatcher.ForceEvent(ctx); err != nil {
		t.Fatal(err)
	}
	ctx, err = namespaces.InitContext(ctx, configWatcher.SelectorChannel())
	if err != nil {
		t.Fatal(err)
	}

	f := informer.GetK8sFactory(ctx, "")
	f.Core().V1().Services().Informer()
	f.Core().V1().ConfigMaps().Informer()
	f.Core().V1().Pods().Informer()
	f.Apps().V1().Deployments().Informer()
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())

	env := managerutil.Env{
		ManagerNamespace: mgrNs,
		MaxReceiveSize:   resource.Quantity{},
		PodCIDRStrategy:  "environment",
		PodCIDRs: []netip.Prefix{
			netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, 0, 0}), 16),
		},
	}
	ctx = managerutil.WithEnv(ctx, &env)
	ctx = mutator.WithMap(ctx, mutator.Load(ctx))

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	agentmap.GeneratorConfigFunc = env.GeneratorConfig
	s := grpc.NewServer()
	mgr, g, err := NewServiceFunc(ctx, configWatcher)
	if err != nil {
		t.Fatalf("failed to build manager: %v", err)
	}
	mgr.RegisterServers(s)
	sc := &dhttp.ServerConfig{
		Handler: s,
	}
	err = configWatcher.ForceEvent(ctx)
	if err != nil {
		t.Fatalf("configMap watcher failed: %v", err)
	}

	shutdownServer := func() {}
	g.Go("server", func(ctx context.Context) error {
		defer cancel()
		var serverCtx context.Context
		serverCtx, shutdownServer = context.WithCancel(ctx)
		return sc.Serve(serverCtx, lis)
	})
	t.Cleanup(func() {
		shutdownServer()
		if err := g.Wait(); err != nil && err != ctx.Err() {
			t.Error(err)
		}
	})
	return conn
}

func Test_hasDomainSuffix(t *testing.T) {
	tests := []struct {
		name   string
		qn     string
		suffix string
		want   bool
	}{
		{
			"empty suffix",
			"aa.bb.",
			"",
			false,
		},
		{
			"suffix with dot",
			"aa.bb.",
			"bb.",
			true,
		},
		{
			"suffix without dot",
			"aa.bb.",
			"bb",
			true,
		},
		{
			"suffix partial match",
			"aa.bb.",
			"b.",
			false,
		},
		{
			"suffix partial match no dot",
			"foo.bar.",
			"b",
			false,
		},
		{
			"name without dot",
			"aa.bb",
			"bb",
			false,
		},
		{
			"equal",
			"a.",
			"a.",
			true,
		},
		{
			"equal no dot",
			"a.",
			"a",
			true,
		},
		{
			"empty qn",
			".",
			"a",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDomainSuffix(tt.qn, tt.suffix); got != tt.want {
				t.Errorf("hasDomainSuffix() = %v, want %v", got, tt.want)
			}
		})
	}
}
