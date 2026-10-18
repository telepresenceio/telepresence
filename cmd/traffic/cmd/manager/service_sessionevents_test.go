package manager

import (
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/types"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
)

// recvAgentPodsDelta receives from wse until it gets a message whose
// AgentPods field carries an actual upsert or removal. The underlying
// cache.Map subscription (shared with watchAgentPodsDelta) always sends an
// initial snapshot on subscribe -- even an empty one -- so a non-empty check
// is needed to skip past it and past any interleaved intercepts-only
// message.
func recvAgentPodsDelta(t *testing.T, wse rpc.Manager_WatchSessionEventsClient) *rpc.AgentPodInfoDelta {
	t.Helper()
	for {
		delta, err := wse.Recv()
		require.NoError(t, err)
		if delta.AgentPods != nil && (len(delta.AgentPods.Upserts) > 0 || len(delta.AgentPods.Removals) > 0) {
			return delta.AgentPods
		}
	}
}

// recvInterceptsDelta receives from wse until it gets a message whose
// Intercepts field carries an actual upsert or removal. Unlike the
// agent-pods branch, the intercepts branch mirrors WatchInterceptsDelta and
// always sends -- including an empty message for the initial (empty)
// subscription snapshot -- so a non-empty check is needed to skip past it.
func recvInterceptsDelta(t *testing.T, wse rpc.Manager_WatchSessionEventsClient) *rpc.InterceptInfoDelta {
	t.Helper()
	for {
		delta, err := wse.Recv()
		require.NoError(t, err)
		if delta.Intercepts != nil && (len(delta.Intercepts.Upserts) > 0 || len(delta.Intercepts.Removals) > 0) {
			return delta.Intercepts
		}
	}
}

// TestWatchSessionEvents_AgentInConnectedNamespace proves that an agent
// arriving in the client's connected namespace is delivered on the
// agent-pods side of the multiplexed stream, projected into AgentPodInfo
// (workload name, pod name, namespace, pod IP, api port, and the agent's
// version).
func TestWatchSessionEvents_AgentInConnectedNamespace(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	conn, _, _ := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{Session: aliceSess})
	req.NoError(err)

	helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	helloAgent.PodIp = "10.1.2.3"
	_, err = client.ArriveAsAgent(ctx, helloAgent)
	req.NoError(err)

	agentPods := recvAgentPodsDelta(t, wse)
	req.Len(agentPods.Upserts, 1)
	for _, a := range agentPods.Upserts {
		req.Equal(helloAgent.Name, a.WorkloadName)
		req.Equal(helloAgent.PodName, a.PodName)
		req.Equal(helloAgent.Namespace, a.Namespace)
		ip, ok := netip.AddrFromSlice(a.PodIp)
		req.True(ok)
		req.Equal(helloAgent.PodIp, ip.String())
		req.Equal(helloAgent.ApiPort, a.ApiPort)
		req.Equal(helloAgent.Version, a.Version, "the agent's version must be carried on the projected AgentPodInfo")
	}
}

// TestWatchSessionEvents_AgentInRequestedNamespace proves that an agent in a
// namespace that was requested (via SessionEventsRequest.Namespaces) but is
// not the client's connected namespace arrives too -- there is no slimming
// concept on this stream: AgentPodInfo is always fully populated, whatever
// namespace the agent is in.
func TestWatchSessionEvents_AgentInRequestedNamespace(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	conn, _, _ := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{
		Session:    aliceSess,
		Namespaces: []string{"other"},
	})
	req.NoError(err)

	otherAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	otherAgent.Namespace = "other"
	otherAgent.PodUid = "other-ns-pod-uid"
	otherAgent.PodIp = "10.1.2.3"
	_, err = client.ArriveAsAgent(ctx, otherAgent)
	req.NoError(err)

	agentPods := recvAgentPodsDelta(t, wse)
	req.Len(agentPods.Upserts, 1)
	for _, a := range agentPods.Upserts {
		req.Equal("other", a.Namespace)
		req.Equal(otherAgent.Version, a.Version)
	}
}

// TestWatchSessionEvents_AgentOutsideRequestedNamespaceExcluded proves that
// an agent whose namespace is neither the connected namespace nor among the
// requested namespaces is never delivered on the stream.
func TestWatchSessionEvents_AgentOutsideRequestedNamespaceExcluded(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	conn, _, _ := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{
		Session:    aliceSess,
		Namespaces: []string{"other"},
	})
	req.NoError(err)

	// This agent's namespace is not requested and not the connected namespace:
	// it must never produce a delta message on this stream.
	excludedAgent := proto.Clone(testAgents["demo1"]).(*rpc.AgentInfo)
	excludedAgent.Namespace = "excluded"
	excludedAgent.PodUid = "excluded-ns-pod-uid"
	excludedAgent.PodIp = "10.1.2.5"
	_, err = client.ArriveAsAgent(ctx, excludedAgent)
	req.NoError(err)

	// A sentinel agent in the connected namespace is used to prove that the
	// stream is alive and that the excluded agent's arrival produced no
	// message of its own -- the first (and only) delta received must be the
	// sentinel.
	sentinel := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	sentinel.PodIp = "10.1.2.3"
	_, err = client.ArriveAsAgent(ctx, sentinel)
	req.NoError(err)

	agentPods := recvAgentPodsDelta(t, wse)
	req.Len(agentPods.Upserts, 1)
	for _, a := range agentPods.Upserts {
		req.Equal(sentinel.PodUid, a.PodId, "the excluded-namespace agent must not be delivered")
	}
}

// TestWatchSessionEvents_OwnInterceptOnly proves that the intercepts side of
// the multiplexed stream carries this client's own intercepts, and not
// another client's.
func TestWatchSessionEvents_OwnInterceptOnly(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)

	conn, mgr, svcCtx := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)
	bobSess, err := client.ArriveAsClient(ctx, testClients["bob"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{Session: aliceSess})
	req.NoError(err)

	aliceIntercept := &rpc.InterceptInfo{
		Id: aliceSess.SessionId + ":alice-intercept",
		Spec: &rpc.InterceptSpec{
			Name:      "alice-intercept",
			Client:    testClients["alice"].Name,
			Agent:     "hello",
			Namespace: "default",
			Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_WAITING,
		ClientSession: aliceSess,
	}
	bobIntercept := &rpc.InterceptInfo{
		Id: bobSess.SessionId + ":bob-intercept",
		Spec: &rpc.InterceptSpec{
			Name:      "bob-intercept",
			Client:    testClients["bob"].Name,
			Agent:     "hello",
			Namespace: "default",
			Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_WAITING,
		ClientSession: bobSess,
	}
	// Bob's intercept is restored first: since it is filtered out at the
	// subscription level (not Alice's own), it must produce no delta message
	// of its own -- the first non-empty delta Alice's stream receives must be
	// for her own intercept, restored second. RestoreIntercepts is given the
	// server's own context (not the test's) because it looks up the target
	// workload via the k8s client installed on that context.
	mgr.State().RestoreIntercepts(svcCtx, []*rpc.InterceptInfo{bobIntercept}, time.Now())
	mgr.State().RestoreIntercepts(svcCtx, []*rpc.InterceptInfo{aliceIntercept}, time.Now())

	intercepts := recvInterceptsDelta(t, wse)
	req.Len(intercepts.Upserts, 1)
	for _, ii := range intercepts.Upserts {
		req.Equal(aliceIntercept.Id, ii.Id, "only the client's own intercept must be delivered")
	}
}

// TestWatchSessionEvents_InterceptedFlipsOnActiveIntercept proves that once
// the watching client's intercept for a workload transitions to ACTIVE, the
// agent-pod projection for that workload's pod is re-sent with
// Intercepted == true -- driven by the refresh-intercepted channel, exactly
// as watchAgentPodsDelta behaves.
func TestWatchSessionEvents_InterceptedFlipsOnActiveIntercept(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	conn, mgr, svcCtx := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{Session: aliceSess})
	req.NoError(err)

	helloAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	helloAgent.PodIp = "10.1.2.3"
	_, err = client.ArriveAsAgent(ctx, helloAgent)
	req.NoError(err)

	agentPods := recvAgentPodsDelta(t, wse)
	req.Len(agentPods.Upserts, 1)
	for _, a := range agentPods.Upserts {
		req.False(a.Intercepted, "must not be intercepted before any intercept exists")
	}

	aliceIntercept := &rpc.InterceptInfo{
		Id: aliceSess.SessionId + ":alice-intercept",
		Spec: &rpc.InterceptSpec{
			Name:      "alice-intercept",
			Client:    testClients["alice"].Name,
			Agent:     helloAgent.Name,
			Namespace: helloAgent.Namespace,
			Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		PodIp:         helloAgent.PodIp,
		ClientSession: aliceSess,
	}
	mgr.State().RestoreIntercepts(svcCtx, []*rpc.InterceptInfo{aliceIntercept}, time.Now())

	// The restore drives both the intercepts field (own intercept, ACTIVE)
	// and, via the refresh-intercepted channel, a re-sent agent-pod upsert.
	// Read agent-pod deltas (skipping any interleaved intercepts-field
	// message) until the workload's Intercepted flag is seen flipped to true.
	for {
		delta := recvAgentPodsDelta(t, wse)
		flipped := false
		for _, a := range delta.Upserts {
			if a.WorkloadName == helloAgent.Name && a.Intercepted {
				flipped = true
			}
		}
		if flipped {
			break
		}
	}
}

// TestWatchSessionEvents_StreamEndsOnSessionDone proves that the stream ends
// cleanly (io.EOF, no error) when the watching client's session is removed.
func TestWatchSessionEvents_StreamEndsOnSessionDone(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)

	conn, _, _ := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{Session: aliceSess})
	req.NoError(err)

	// Wait for the handler's initial (empty) intercepts snapshot so the
	// server-side subscriptions are known to be armed; departing before the
	// handler has looked the session up makes it error out rather than end
	// cleanly, which is correct behavior but not what this test is about.
	_, err = wse.Recv()
	req.NoError(err)

	_, err = client.Depart(ctx, aliceSess)
	req.NoError(err)

	// Drain any messages already in flight (e.g. the initial, empty
	// intercepts snapshot) before the stream closes; the terminal error must
	// be io.EOF, not something else.
	for {
		_, err = wse.Recv()
		if err != nil {
			break
		}
	}
	req.ErrorIs(err, io.EOF, "the stream must end cleanly when the client session is removed")
}

// TestWatchSessionEvents_InactiveAgentFiltered proves that an agent whose
// pod has been marked inactive via mutator.Map.Inactivate is never delivered
// as an upsert.
func TestWatchSessionEvents_InactiveAgentFiltered(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	testClients := testdata.GetTestClients(t)
	testAgents := testdata.GetTestAgents(t)

	conn, mgr, svcCtx := getTestClientConnAndService(ctx, t, nil)
	defer conn.Close()
	client := rpc.NewManagerClient(conn)

	aliceSess, err := client.ArriveAsClient(ctx, testClients["alice"])
	req.NoError(err)

	wse, err := client.WatchSessionEvents(ctx, &rpc.SessionEventsRequest{Session: aliceSess})
	req.NoError(err)

	inactiveAgent := proto.Clone(testAgents["hello"]).(*rpc.AgentInfo)
	inactiveAgent.PodUid = "inactive-pod-uid"

	// Mark the pod inactive before the agent's AgentSession ever lands in state.
	// ArriveAsAgent (which calls state.AddAgent) itself refuses to add an agent
	// for an already-inactive pod, so RestoreAgents -- which performs no such
	// check -- is used to get the (inactive) AgentSession into state directly,
	// the way a manager restart would restore agents that raced with an
	// eviction.
	mutator.GetMap(svcCtx).Inactivate(types.UID(inactiveAgent.PodUid))
	mgr.State().RestoreAgents([]*rpc.AgentInfo{inactiveAgent}, time.Now())

	// A sentinel arrives normally right after; it proves the stream is alive
	// and that the inactive agent's upsert produced no message of its own.
	sentinel := proto.Clone(testAgents["demo1"]).(*rpc.AgentInfo)
	sentinel.PodIp = "10.1.2.6"
	_, err = client.ArriveAsAgent(ctx, sentinel)
	req.NoError(err)

	agentPods := recvAgentPodsDelta(t, wse)
	req.Len(agentPods.Upserts, 1)
	for _, a := range agentPods.Upserts {
		req.Equal(sentinel.PodUid, a.PodId, "the inactive agent must not be delivered")
	}
}
