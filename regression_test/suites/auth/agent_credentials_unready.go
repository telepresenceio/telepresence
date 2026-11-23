package auth

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardCredentials proves a traffic-agent that has not received session credentials from
// the traffic-manager refuses callers instead of accepting them.
type AgenthardCredentials struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardCredentials{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

const (
	agenthardPollInterval   = time.Second
	agenthardManagerTimeout = 120 * time.Second
	agenthardRestartTimeout = 90 * time.Second
	agenthardServeTimeout   = 60 * time.Second
	agenthardCallTimeout    = 10 * time.Second
)

var agenthardListenerRE = regexp.MustCompile(`Listener opened on \S*:(\d+)`)

// Test_AgentRefusesCallersWithoutCredentials restarts the traffic-agent of an injected pod
// while the traffic-manager is scaled to zero, so the agent cannot fetch credentials. A
// WatchDial call without a session token, sent through the API server's port-forward, is
// then refused with Unavailable and a message naming the missing credential material.
func (s *AgenthardCredentials) Test_AgentRefusesCallersWithoutCredentials() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(managers.AuthEnforcing()))
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("agenthard-credentials"))

	a := conn.Ingest(t, wl, cli.MountFalse())
	a.Detach(t)
	pod := agenthardAgentPod(ctx, r, ns, wl.Name)
	s.Require().NotEmpty(pod, "%s should have a traffic-agent", wl.Name)
	before := agenthardRestartCount(ctx, r, ns, pod)

	agenthardScaleManagerToZero(t, ctx, r)

	// The exec session can end with an error when the container exits under it.
	if _, err := r.Kubectl(ctx, ns, "exec", pod, "-c", agentContainer, "--", "sh", "-c", "kill 1"); err != nil {
		r.Infof("[rtest] auth: stopping the traffic-agent: %v", err)
	}
	s.Require().Eventually(func() bool {
		return agenthardRestartCount(ctx, r, ns, pod) > before && agenthardAgentRunning(ctx, r, ns, pod)
	}, agenthardRestartTimeout, agenthardPollInterval, "the traffic-agent container should restart")

	var port uint16
	s.Require().Eventually(func() bool {
		logs, err := r.Kubectl(ctx, ns, "logs", pod, "-c", agentContainer)
		if err != nil {
			return false
		}
		m := agenthardListenerRE.FindStringSubmatch(logs)
		if m == nil {
			return false
		}
		p, err := strconv.ParseUint(m[1], 10, 16)
		port = uint16(p)
		return err == nil
	}, agenthardRestartTimeout, agenthardPollInterval, "the restarted traffic-agent should log its gRPC port")

	dial, err := rt.AgenthardPodDialer(rt.Env{Ctx: ctx, T: t, R: r})
	s.Require().NoError(err)
	cc, err := grpc.NewClient("passthrough:///agenthard-agent",
		grpc.WithContextDialer(func(dctx context.Context, _ string) (net.Conn, error) {
			return dial(dctx, ns, pod, port)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	s.Require().NoError(err)
	t.Cleanup(func() { _ = cc.Close() })
	ac := agent.NewAgentClient(cc)

	s.Require().Eventually(func() bool {
		vctx, cancel := context.WithTimeout(ctx, agenthardCallTimeout)
		defer cancel()
		_, err := ac.Version(vctx, &emptypb.Empty{})
		return err == nil
	}, agenthardServeTimeout, agenthardPollInterval, "the restarted traffic-agent should serve gRPC")

	cctx, cancel := context.WithTimeout(ctx, agenthardCallTimeout)
	defer cancel()
	stream, err := ac.WatchDial(cctx, &manager.SessionInfo{SessionId: "agenthard-unknown-session"})
	if err == nil {
		_, err = stream.Recv()
	}
	s.Require().Error(err, "WatchDial without credentials should be refused")
	s.Equal(codes.Unavailable, status.Code(err), "WatchDial error: %v", err)
	s.Contains(err.Error(), "credential", "WatchDial error: %v", err)
}

// agenthardAgentPod returns the name of a live pod of the workload name in ns that has a
// traffic-agent container.
func agenthardAgentPod(ctx context.Context, r *rt.Runtime, ns, name string) string {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		`jsonpath={range .items[*]}{.metadata.name}|{.metadata.deletionTimestamp}|{.spec.containers[*].name}{"\n"}{end}`)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || parts[1] != "" {
			continue
		}
		for _, c := range strings.Fields(parts[2]) {
			if c == agentContainer {
				return parts[0]
			}
		}
	}
	return ""
}

func agenthardRestartCount(ctx context.Context, r *rt.Runtime, ns, pod string) int {
	out, err := r.Kubectl(ctx, ns, "get", "pod", pod, "-o",
		fmt.Sprintf(`jsonpath={.status.containerStatuses[?(@.name=="%s")].restartCount}`, agentContainer))
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return n
}

func agenthardAgentRunning(ctx context.Context, r *rt.Runtime, ns, pod string) bool {
	out, err := r.Kubectl(ctx, ns, "get", "pod", pod, "-o",
		fmt.Sprintf(`jsonpath={.status.containerStatuses[?(@.name=="%s")].state.running.startedAt}`, agentContainer))
	return err == nil && strings.TrimSpace(out) != ""
}

// agenthardScaleManagerToZero scales the traffic-manager to zero replicas, waits until its
// pod is gone, and registers a cleanup that restores the replica count and waits for the
// rollout.
func agenthardScaleManagerToZero(t *testing.T, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	ref := "statefulset/traffic-manager"
	if _, err := r.Kubectl(ctx, mgrNS, "get", ref, "-o", "name"); err != nil {
		ref = "deploy/traffic-manager"
	}
	out, err := r.Kubectl(ctx, mgrNS, "get", ref, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		t.Fatalf("reading replicas of %s: %v", ref, err)
	}
	replicas := strings.TrimSpace(out)
	if replicas == "" || replicas == "0" {
		replicas = "1"
	}
	t.Cleanup(func() {
		if _, err := r.Kubectl(ctx, mgrNS, "scale", ref, "--replicas="+replicas); err != nil {
			t.Errorf("restoring %s: %v", ref, err)
			return
		}
		if _, err := r.Kubectl(ctx, mgrNS, "rollout", "status", ref, "--timeout="+agenthardManagerTimeout.String()); err != nil {
			t.Errorf("waiting for %s: %v", ref, err)
		}
	})
	if _, err := r.Kubectl(ctx, mgrNS, "scale", ref, "--replicas=0"); err != nil {
		t.Fatalf("scaling %s to zero: %v", ref, err)
	}
	deadline := time.Now().Add(agenthardManagerTimeout)
	for time.Now().Before(deadline) {
		pods, err := r.Kubectl(ctx, mgrNS, "get", "pods", "-l", "app=traffic-manager", "-o", "name")
		if err == nil && strings.TrimSpace(pods) == "" {
			return
		}
		time.Sleep(agenthardPollInterval)
	}
	t.Fatalf("the traffic-manager pod did not terminate within %s", agenthardManagerTimeout)
}
