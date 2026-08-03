package mounts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// podTermTimeout/podTermPollInterval bound the wait for a scaled-down
// workload's pods to actually terminate: `rollout status` alone would report
// success as soon as the scale-to-zero is accepted, before the old pod (and
// its traffic-agent sidecar) is actually gone (mirrors suites/quic/
// helpers.go's scaleQuicForwarderDown). Generous, since pod eviction can
// take up to 2 minutes.
const (
	podTermTimeout      = 2 * time.Minute
	podTermPollInterval = 5 * time.Second
)

// mountRecoveryTimeout bounds the post-scale-up EventuallyFile/RoutedToLocal
// checks: re-establishing the FUSE/SFTP mount and routing to the restarted
// agent both take longer than the ordinary mountTimeout -- the mount alone
// needs a 30s grace period, so 60s covers both.
const mountRecoveryTimeout = 60 * time.Second

// Podscaling proves a suite-long intercept+mount survives its workload being
// scaled to zero and back: the mount recovers and its content is readable
// again, and routing to the local handler keeps working.
type Podscaling struct {
	rt.Suite
}

func init() {
	rt.Register(&Podscaling{}, rt.InArea("mounts"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE), rt.NotOn("windows"), rt.WithLabels(rt.Slow))
}

func (s *Podscaling) Test_MountSurvivesPodScaling() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoWithConfigVolume("mounts-podscaling"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"))
	defer a.Detach(t)

	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)
	url := wl.ServiceURL()
	rt.RoutedToLocal(t, url, ls)

	kindPath := strings.ToLower(wl.Kind) + "/" + wl.Name
	selector := "app=" + wl.Name

	if _, err := r.Kubectl(ctx, wl.Namespace, "scale", kindPath, "--replicas", "0"); err != nil {
		t.Fatalf("scale %s to 0: %v", wl.Name, err)
	}
	waitForNoPods(t, ctx, r, wl.Namespace, selector)

	if _, err := r.Kubectl(ctx, wl.Namespace, "scale", kindPath, "--replicas", "1"); err != nil {
		t.Fatalf("scale %s to 1: %v", wl.Name, err)
	}
	if _, err := r.Kubectl(ctx, wl.Namespace, "rollout", "status", kindPath, "--timeout=120s"); err != nil {
		t.Fatalf("rollout status %s: %v", wl.Name, err)
	}

	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountRecoveryTimeout)
	rt.RoutedToLocal(t, url, ls)
}

// waitForNoPods polls until no pod matching selector remains in ns, or fails
// t once podTermTimeout elapses.
func waitForNoPods(t testing.TB, ctx context.Context, r *rt.Runtime, ns, selector string) {
	t.Helper()
	deadline := time.Now().Add(podTermTimeout)
	for {
		out, err := r.Kubectl(ctx, ns, "get", "pods", "-l", selector, "-o", "name")
		if err == nil && strings.TrimSpace(out) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pods matching %s did not terminate after scaling to 0", selector)
		}
		time.Sleep(podTermPollInterval)
	}
}
