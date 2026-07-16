package manager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	fakeDiscovery "k8s.io/client-go/discovery/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	fakeargorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func dumps(o any) string {
	bs, _ := json.Marshal(o)
	return string(bs)
}

func TestConnect(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no bookmark events),
	// which is enabled by default in client-go v0.35+. Disable it for this test.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	require := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	version.Version, version.Structured = version.Init("0.0.0-testing", "TELEPRESENCE_VERSION")

	conn := getTestClientConn(ctx, t, nil)
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

// TestWatchQuicBackends proves the backend-allowlist RPC's subscribe/update
// contract required by the QUIC forwarder design
// (docs/reference/quic-transport-architecture.md, "The forwarder"): an
// immediate initial snapshot containing the
// traffic-manager's own pod IP, and a further, full-replacement snapshot
// whenever an agent session arrives or departs. The call is deliberately made
// with no SessionInfo -- the forwarder has no client session.
func TestWatchQuicBackends(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testAgents := testdata.GetTestAgents(t)

	conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) { e.TunnelQuicPort = 7778 })
	defer conn.Close()

	client := rpc.NewManagerClient(conn)

	wqb, err := client.WatchQuicBackends(ctx, &empty.Empty{})
	req.NoError(err)

	// Initial snapshot: just this traffic-manager's own pod IP and port.
	snap, err := wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 1)
	req.Equal("manager", snap.Backends[0].Kind)
	req.Equal(int32(7778), snap.Backends[0].Port)
	mgrIP, ok := netip.AddrFromSlice(snap.Backends[0].Ip)
	req.True(ok)
	req.Equal("10.0.0.9", mgrIP.String())

	// An agent with no QUIC listener of its own (QuicPort == 0) arrives: it
	// must NOT join the allowlist -- a port-0 "backend" cannot be dialed. It
	// arrives immediately before the QUIC-enabled agent below so that both
	// changes coalesce into the single debounced snapshot asserted next; that
	// snapshot must reflect its exclusion.
	noQuicAgent := proto.Clone(testAgents["helloPro"]).(*rpc.AgentInfo)
	noQuicAgent.PodIp = "10.1.2.4"
	_, err = client.ArriveAsAgent(ctx, noQuicAgent)
	req.NoError(err)

	// An agent with a QUIC listener arrives; its pod IP, port, and pod UID all
	// join the allowlist.
	helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	helloAgent.PodIp = "10.1.2.3"
	helloAgent.QuicPort = 7787
	helloSess, err := client.ArriveAsAgent(ctx, helloAgent)
	req.NoError(err)

	snap, err = wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 2)
	var sawManager, sawAgent bool
	for _, b := range snap.Backends {
		ip, ok := netip.AddrFromSlice(b.Ip)
		req.True(ok)
		switch b.Kind {
		case "manager":
			sawManager = true
			req.Equal("10.0.0.9", ip.String())
			req.Equal(int32(7778), b.Port)
		case "agent":
			sawAgent = true
			req.Equal("10.1.2.3", ip.String())
			req.Equal(int32(7787), b.Port)
			req.Equal(helloAgent.PodUid, b.PodUid)
		default:
			t.Fatalf("unexpected backend kind %q", b.Kind)
		}
	}
	req.True(sawManager)
	req.True(sawAgent)

	// The agent departs; the allowlist shrinks back to just the manager.
	_, err = client.Depart(ctx, helloSess)
	req.NoError(err)

	snap, err = wqb.Recv()
	req.NoError(err)
	req.Len(snap.Backends, 1)
	req.Equal("manager", snap.Backends[0].Kind)
}

// TestGetQuicAgentCert covers the three cases the RPC's contract distinguishes,
// per "Agent connections over QUIC" in docs/reference/quic-transport-architecture.md: an agent
// session gets a certificate that verifies against the manager's QUIC CA for exactly
// its own SNI name; a manager with no QUIC CA reports enabled=false rather than
// erroring; and a client (non-agent) session is rejected outright, regardless of
// whether the QUIC CA exists.
func TestGetQuicAgentCert(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	testAgents := testdata.GetTestAgents(t)
	testClients := testdata.GetTestClients(t)

	t.Run("agent session with QUIC CA", func(t *testing.T) {
		req := require.New(t)
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) { e.TunnelQuicPort = 7778 })
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		agentSess, err := client.ArriveAsAgent(ctx, helloAgent)
		req.NoError(err)

		cert, err := client.GetQuicAgentCert(ctx, agentSess)
		req.NoError(err)
		req.True(cert.Enabled)
		wantSNI := quicfwd.AgentSNI(helloAgent.PodUid)
		req.Equal(wantSNI, cert.Sni)

		roots := x509.NewCertPool()
		req.True(roots.AppendCertsFromPEM(cert.CaPem))
		pair, err := tls.X509KeyPair(cert.CertPem, cert.KeyPem)
		req.NoError(err)
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		req.NoError(err)
		_, err = leaf.Verify(x509.VerifyOptions{
			Roots:     roots,
			DNSName:   wantSNI,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		req.NoError(err, "minted agent certificate must verify against the manager's QUIC CA for its own SNI name")
	})

	t.Run("no QUIC CA", func(t *testing.T) {
		req := require.New(t)
		// TunnelQuicPort defaults to 0 here, so NewService never creates a
		// QUIC CA at all (see service.go's NewService).
		conn := getTestClientConn(ctx, t, nil)
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
		agentSess, err := client.ArriveAsAgent(ctx, helloAgent)
		req.NoError(err)

		cert, err := client.GetQuicAgentCert(ctx, agentSess)
		req.NoError(err)
		req.False(cert.Enabled)
	})

	t.Run("non-agent session", func(t *testing.T) {
		req := require.New(t)
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) { e.TunnelQuicPort = 7778 })
		defer conn.Close()
		client := rpc.NewManagerClient(conn)

		clientSess, err := client.ArriveAsClient(ctx, testClients["alice"])
		req.NoError(err)

		_, err = client.GetQuicAgentCert(ctx, clientSess)
		req.Error(err)
	})
}

// TestGetQuicTunnelEndpoint_Gating covers the three cases "Zero-configuration endpoint
// discovery" (docs/reference/quic-transport-architecture.md) distinguishes: an explicit
// externalHost override always wins and bypasses discovery outright; discovery
// candidates alone are sufficient to enable the endpoint when no override is set; and
// neither present means Enabled stays false.
func TestGetQuicTunnelEndpoint_Gating(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	testClients := testdata.GetTestClients(t)

	arriveAndFetch := func(t *testing.T, conn *grpc.ClientConn) *rpc.QuicTunnelEndpoint {
		t.Helper()
		req := require.New(t)
		client := rpc.NewManagerClient(conn)
		sess, err := client.ArriveAsClient(ctx, testClients["alice"])
		req.NoError(err)
		ep, err := client.GetQuicTunnelEndpoint(ctx, sess)
		req.NoError(err)
		return ep
	}

	t.Run("explicit override, no discovery", func(t *testing.T) {
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicExternalHost = "quic.example.com"
			// A discovered candidate must be ignored in favor of the override:
			// deliberately do NOT set TunnelQuicServiceName, so if the gate ever
			// regressed to consulting discovery first this would fail loudly
			// (Enabled would be false) rather than silently picking the wrong host.
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.True(t, ep.Enabled)
		require.Equal(t, "quic.example.com", ep.Host)
		require.Equal(t, int32(7778), ep.Port)
		require.Len(t, ep.Candidates, 1)
		require.Equal(t, "quic.example.com", ep.Candidates[0].Host)
	})

	t.Run("discovery candidates present, no override", func(t *testing.T) {
		mgrNs := "ambassador"
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager-quic", Namespace: mgrNs},
			Spec: corev1.ServiceSpec{
				Type:  corev1.ServiceTypeLoadBalancer,
				Ports: []corev1.ServicePort{{Port: 7778}},
			},
			Status: corev1.ServiceStatus{
				LoadBalancer: corev1.LoadBalancerStatus{
					Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.9"}},
				},
			},
		}
		conn := getTestClientConn(ctx, t, []runtime.Object{svc}, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicServiceName = "traffic-manager-quic"
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.True(t, ep.Enabled)
		require.Equal(t, "203.0.113.9", ep.Host)
		require.Equal(t, int32(7778), ep.Port)
		require.Len(t, ep.Candidates, 1)
		require.Equal(t, "203.0.113.9", ep.Candidates[0].Host)
	})

	t.Run("neither override nor discovery candidates", func(t *testing.T) {
		conn := getTestClientConn(ctx, t, nil, func(e *managerutil.Env) {
			e.TunnelQuicPort = 7778
			e.TunnelQuicServiceName = "traffic-manager-quic"
			// No Service seeded in the fake clientset: discovery starts (the
			// service name is set) but finds nothing, and there is no override.
		})
		defer conn.Close()

		ep := arriveAndFetch(t, conn)
		require.False(t, ep.Enabled)
	})
}

func getTestClientConn(ctx context.Context, t *testing.T, extraObjects []runtime.Object, envMods ...func(*managerutil.Env)) *grpc.ClientConn {
	conn, _, _ := getTestClientConnAndService(ctx, t, extraObjects, envMods...)
	return conn
}

// getTestClientConnAndService is identical to getTestClientConn, but also
// returns the manager Service (for direct state access, e.g. RestoreIntercepts)
// and the context the server runs with (for direct mutator.Map access, e.g.
// marking a pod inactive via mutator.GetMap(ctx).Inactivate).
func getTestClientConnAndService(
	ctx context.Context, t *testing.T, extraObjects []runtime.Object, envMods ...func(*managerutil.Env),
) (*grpc.ClientConn, Service, context.Context) {
	const bufsize = 64 * 1024
	var cancel func()
	ctx, cancel = context.WithCancel(ctx)

	lis := bufconn.Listen(bufsize)
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	seedObjects := append([]runtime.Object{&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
			Labels: map[string]string{
				labels.NameLabelKey: "default",
			},
		},
	}}, extraObjects...)
	fakeClient := fake.NewClientset(seedObjects...)
	k8sapi.InstallFakeSelfSubjectAccessReviews(fakeClient, nil)
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
		Data: map[string]string{
			"namespace-selector.yaml": `
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: In
  values:
    - default
    - other
`,
			"agent-state.yaml": ``,
		},
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
		ManagerNamespace:   mgrNs,
		GrpcMaxReceiveSize: resource.Quantity{},
		PodCidrStrategy:    "environment",
		PodCidrs: []netip.Prefix{
			netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, 0, 0}), 16),
		},
		PodIp:                     netip.AddrFrom4([4]byte{10, 0, 0, 9}),
		AgentInitContainerEnabled: true,
		AgentMaxIdleTime:          24 * time.Hour,
		ClientConnectionTTL:       24 * time.Minute,
	}
	for _, mod := range envMods {
		mod(&env)
	}
	ctx = managerutil.WithEnv(ctx, &env)
	ctx = mutator.WithMap(ctx, mutator.Load(ctx))

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	s := server.New(ctx)
	g := log.NewGroup(ctx)
	mgr, err := NewService(ctx, g, configWatcher)
	if err != nil {
		t.Fatalf("failed to build manager: %v", err)
	}
	mgr.RegisterServers(s)
	err = configWatcher.ForceEvent(ctx)
	if err != nil {
		t.Fatalf("configMap watcher failed: %v", err)
	}

	g.Go("server", func(ctx context.Context) error {
		defer cancel()
		return s.Serve(lis)
	})
	t.Cleanup(func() {
		s.GracefulStop()
		if err := g.Wait(); err != nil {
			t.Error(err)
		}
	})
	return conn, mgr, ctx
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
