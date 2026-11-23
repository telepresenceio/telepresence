package session

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	malformedManagerSelector = "app=traffic-manager"
	malformedManagerPodName  = "traffic-manager"
	malformedAgentContainer  = "traffic-agent"
	malformedReadyTimeout    = 60 * time.Second
	malformedCallTimeout     = 20 * time.Second
	malformedWatchPeriod     = 6 * time.Second
	malformedPollInterval    = time.Second
)

// MalformedMessages sends malformed gRPC messages (an empty tunnel message,
// requests lacking nested messages) over a raw connection to the
// traffic-manager and to a traffic-agent, and asserts that the receiving
// container keeps running.
type MalformedMessages struct {
	rt.Suite
}

func init() {
	rt.Register(&MalformedMessages{}, rt.InArea("session"), rt.NeedsManager(managers.Default))
}

// malformedTarget identifies one container whose survival is asserted.
type malformedTarget struct {
	r         *rt.Runtime
	ns        string
	selector  string
	container string
	// broken is set once a pod did not become Ready in time, so that the
	// remaining cases fail fast.
	broken bool
}

func (g *malformedTarget) jsonpath(ctx context.Context, expr string) (string, error) {
	out, err := g.r.Kubectl(ctx, g.ns, "get", "pod", "-l", g.selector, "-o", "jsonpath="+expr)
	return strings.TrimSpace(out), err
}

// restarts returns the restart count of the container.
func (g *malformedTarget) restarts(ctx context.Context) (int, error) {
	out, err := g.jsonpath(ctx, fmt.Sprintf(`{.items[0].status.containerStatuses[?(@.name=="%s")].restartCount}`, g.container))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// healthy reports whether the pod is Ready and the container is running.
func (g *malformedTarget) healthy(ctx context.Context) bool {
	ready, err := g.jsonpath(ctx, `{.items[0].status.conditions[?(@.type=="Ready")].status}`)
	if err != nil || ready != "True" {
		return false
	}
	running, err := g.jsonpath(ctx, fmt.Sprintf(`{.items[0].status.containerStatuses[?(@.name=="%s")].state.running.startedAt}`, g.container))
	return err == nil && running != ""
}

func (g *malformedTarget) waitHealthy(t *testing.T, ctx context.Context) {
	t.Helper()
	if g.broken {
		t.Fatalf("%s pod did not recover after an earlier case", g.container)
	}
	deadline := time.Now().Add(malformedReadyTimeout)
	for !g.healthy(ctx) {
		if time.Now().After(deadline) {
			g.broken = true
			t.Fatalf("%s pod not Ready within %s", g.container, malformedReadyTimeout)
		}
		time.Sleep(malformedPollInterval)
	}
}

// check runs call against the pod: the pod must be Ready beforehand, the call
// must be answered with want (codes.OK means any answer), and the container
// must keep running with an unchanged restart count afterwards.
func (g *malformedTarget) check(t *testing.T, ctx context.Context, want codes.Code, call func(context.Context) error) {
	t.Helper()
	g.waitHealthy(t, ctx)
	before, err := g.restarts(ctx)
	if err != nil {
		t.Fatalf("reading restart count: %v", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, malformedCallTimeout)
	defer cancel()
	err = call(callCtx)
	if want != codes.OK {
		if err == nil {
			t.Errorf("call succeeded, want code %s", want)
		} else if got := status.Code(err); got != want {
			t.Errorf("call returned code %s (%v), want %s", got, err, want)
		}
	}

	deadline := time.Now().Add(malformedWatchPeriod)
	for {
		after, err := g.restarts(ctx)
		if err != nil {
			t.Fatalf("reading restart count: %v", err)
		}
		if after != before {
			g.broken = true
			t.Fatalf("%s restarted: restartCount %d -> %d", g.container, before, after)
		}
		if !g.healthy(ctx) {
			g.waitHealthy(t, ctx)
			t.Fatalf("%s stopped being Ready after the call", g.container)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(malformedPollInterval)
	}
}

// malformedRestConfig builds a rest.Config from the run's RTEST_KUBECONFIG
// and RTEST_CONTEXT, the same inputs the framework's own clients use.
func malformedRestConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if p := os.Getenv("RTEST_KUBECONFIG"); p != "" {
		rules.ExplicitPath = p
	}
	overrides := &clientcmd.ConfigOverrides{}
	if c := os.Getenv("RTEST_CONTEXT"); c != "" {
		overrides.CurrentContext = c
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// malformedDialAgent dials the gRPC port of an agent pod over a port-forward.
func malformedDialAgent(ctx context.Context, ns string, ai *manager.AgentPodInfo) (*grpc.ClientConn, error) {
	cfg, err := malformedRestConfig()
	if err != nil {
		return nil, err
	}
	k8sAPI, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	ctx = client.WithConfig(ctx, client.GetDefaultConfig())
	ctx = k8sapi.WithK8sInterface(ctx, k8sAPI)
	ctx = portforward.WithRestConfig(ctx, cfg)
	pa := portforward.PodAddress{Name: ai.PodName, Namespace: ns, PodID: k8sTypes.UID(ai.PodId)}
	return grpcClient.DialGRPC(ctx, portforward.K8sPFScheme+":///"+pa.AddrFor(uint16(ai.ApiPort)),
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}

// Test_ManagerSurvivesMalformedMessages sends an empty tunnel message and
// each request that lacks a nested message to the traffic-manager. The
// traffic-manager answers with the expected code, its container is not
// restarted and its pod stays Ready.
func (s *MalformedMessages) Test_ManagerSurvivesMalformedMessages() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	mc, closeMC, err := rt.ManagerClient(env, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeMC()

	target := &malformedTarget{
		r:         s.R(),
		ns:        managers.ManagerNamespace,
		selector:  malformedManagerSelector,
		container: malformedManagerPodName,
	}

	sess := &manager.SessionInfo{SessionId: "rtest-malformed-no-such-session"}
	tests := []struct {
		name string
		code codes.Code // codes.OK means any answer
		call func(context.Context) error
	}{
		{"Tunnel empty message", codes.FailedPrecondition, func(ctx context.Context) error {
			return malformedEmptyTunnelMessage(ctx, mc.Tunnel)
		}},
		{"ReconnectClient session only", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.ReconnectClient(ctx, &manager.ReconnectClientRequest{Session: sess})
			return err
		}},
		{"ReconnectClient empty", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.ReconnectClient(ctx, &manager.ReconnectClientRequest{})
			return err
		}},
		{"ReconnectAgent session only", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.ReconnectAgent(ctx, &manager.ReconnectAgentRequest{Session: sess})
			return err
		}},
		{"ReconnectAgent agent only", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.ReconnectAgent(ctx, &manager.ReconnectAgentRequest{Agent: &manager.AgentInfo{}})
			return err
		}},
		{"ReconnectAgent empty", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.ReconnectAgent(ctx, &manager.ReconnectAgentRequest{})
			return err
		}},
		{"CreateIntercept session only", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.CreateIntercept(ctx, &manager.CreateInterceptRequest{Session: sess})
			return err
		}},
		{"CreateIntercept spec only", codes.NotFound, func(ctx context.Context) error {
			_, err := mc.CreateIntercept(ctx, &manager.CreateInterceptRequest{InterceptSpec: &manager.InterceptSpec{Name: "x"}})
			return err
		}},
		{"CreateIntercept empty", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.CreateIntercept(ctx, &manager.CreateInterceptRequest{})
			return err
		}},
		{"PrepareIntercept session only", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.PrepareIntercept(ctx, &manager.CreateInterceptRequest{Session: sess})
			return err
		}},
		{"PrepareIntercept spec only", codes.NotFound, func(ctx context.Context) error {
			_, err := mc.PrepareIntercept(ctx, &manager.CreateInterceptRequest{InterceptSpec: &manager.InterceptSpec{Name: "x"}})
			return err
		}},
		{"PrepareIntercept empty", codes.InvalidArgument, func(ctx context.Context) error {
			_, err := mc.PrepareIntercept(ctx, &manager.CreateInterceptRequest{})
			return err
		}},
		{"RemoveIntercept name only", codes.NotFound, func(ctx context.Context) error {
			_, err := mc.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: "x"})
			return err
		}},
		{"Lookup empty", codes.OK, func(ctx context.Context) error {
			_, err := mc.Lookup(ctx, &manager.LookupRequest{})
			return err
		}},
		{"EnsureAgent empty", codes.OK, func(ctx context.Context) error {
			_, err := mc.EnsureAgent(ctx, &manager.EnsureAgentRequest{})
			return err
		}},
		{"GetAgentConfig empty", codes.OK, func(ctx context.Context) error {
			_, err := mc.GetAgentConfig(ctx, &manager.AgentConfigRequest{})
			return err
		}},
		{"ResolveServicePort empty", codes.OK, func(ctx context.Context) error {
			_, err := mc.ResolveServicePort(ctx, &manager.ResolveServicePortRequest{})
			return err
		}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			target.check(s.T(), ctx, tt.code, tt.call)
		})
	}
}

// Test_AgentSurvivesEmptyTunnelMessage sends an empty tunnel message to the
// Tunnel call of an injected traffic-agent. The call fails, the agent
// container is not restarted and its pod stays Ready.
func (s *MalformedMessages) Test_AgentSurvivesEmptyTunnelMessage() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	conn := s.Connect()

	wl := s.Workload(workloads.Echo("malformed-agent"))
	a := conn.Ingest(t, wl, cli.MountFalse())
	a.Detach(t)

	target := &malformedTarget{
		r:         r,
		ns:        ns,
		selector:  "app=" + wl.Name,
		container: malformedAgentContainer,
	}
	target.waitHealthy(t, ctx)
	podName, err := target.jsonpath(ctx, `{.items[0].metadata.name}`)
	s.Require().NoError(err)

	env := rt.Env{Ctx: ctx, T: t, R: r}
	mc, closeMC, err := rt.ManagerClient(env, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeMC()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sessionInfo := arriveAsClient(t, watchCtx, mc, "rtest-malformed", ns)
	stream, err := mc.WatchAgentPods(watchCtx, sessionInfo)
	s.Require().NoError(err)

	var info *manager.AgentPodInfo
	found := make(chan struct{})
	go func() {
		defer close(found)
		for {
			snap, err := stream.Recv()
			if err != nil {
				return
			}
			for _, ai := range snap.GetAgents() {
				if ai.PodName == podName && ai.ApiPort != 0 {
					info = ai
					return
				}
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(malformedReadyTimeout):
		cancel()
		<-found
	}
	s.Require().NotNil(info, "the manager did not report an agent for pod %s", podName)

	agentConn, err := malformedDialAgent(ctx, ns, info)
	s.Require().NoError(err)
	defer agentConn.Close()
	ac := agent.NewAgentClient(agentConn)

	target.check(t, ctx, codes.FailedPrecondition, func(ctx context.Context) error {
		return malformedEmptyTunnelMessage(ctx, ac.Tunnel)
	})
}

// malformedEmptyTunnelMessage opens a tunnel stream, sends one message with an
// empty payload, and returns the error that ends the stream.
func malformedEmptyTunnelMessage(
	ctx context.Context,
	open func(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[manager.TunnelMessage, manager.TunnelMessage], error),
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := open(ctx)
	if err != nil {
		return err
	}
	if err = stream.Send(&manager.TunnelMessage{}); err != nil {
		// The server may already have ended the stream; Recv reports why.
		_, rerr := stream.Recv()
		if rerr != nil {
			return rerr
		}
		return err
	}
	_, err = stream.Recv()
	return err
}
