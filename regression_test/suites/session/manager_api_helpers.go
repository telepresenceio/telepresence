package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	mgrapiSettle      = 6 * time.Second
	mgrapiReadyWait   = 90 * time.Second
	mgrapiPollEvery   = time.Second
	mgrapiCallTimeout = 30 * time.Second
)

// mgrapiPodStatus is the summed restart count and the readiness of the
// traffic-manager pod's containers.
type mgrapiPodStatus struct {
	restarts int
	ready    bool
}

func mgrapiManagerStatus(ctx context.Context, r *rt.Runtime) (mgrapiPodStatus, error) {
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "pods", "-l", "app=traffic-manager", "-o",
		`jsonpath={range .items[*].status.containerStatuses[*]}{.restartCount} {.ready}{"\n"}{end}`)
	if err != nil {
		return mgrapiPodStatus{}, err
	}
	st := mgrapiPodStatus{ready: true}
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var restarts int
		var ready bool
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d %t", &restarts, &ready); err != nil {
			continue
		}
		seen++
		st.restarts += restarts
		st.ready = st.ready && ready
	}
	if seen == 0 {
		return mgrapiPodStatus{}, fmt.Errorf("no traffic-manager container status in %q", out)
	}
	return st, nil
}

// mgrapiWatchManager returns the traffic-manager's current restart count
// and registers a cleanup that waits for the pod to be Ready again.
func mgrapiWatchManager(t *testing.T, ctx context.Context, r *rt.Runtime) int {
	t.Helper()
	st, err := mgrapiManagerStatus(ctx, r)
	if err != nil {
		t.Fatalf("reading traffic-manager status: %v", err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(mgrapiReadyWait)
		for time.Now().Before(deadline) {
			if cur, err := mgrapiManagerStatus(ctx, r); err == nil && cur.ready {
				return
			}
			time.Sleep(mgrapiPollEvery)
		}
		r.Infof("[rtest] mgrapi: traffic-manager not Ready after %s", mgrapiReadyWait)
	})
	return st.restarts
}

// mgrapiRequireManagerIntact fails when the traffic-manager's restart count
// differs from before at any point during a short settle period, or when
// the pod is not Ready afterwards.
func mgrapiRequireManagerIntact(t *testing.T, ctx context.Context, r *rt.Runtime, before int) {
	t.Helper()
	deadline := time.Now().Add(mgrapiSettle)
	for time.Now().Before(deadline) {
		if st, err := mgrapiManagerStatus(ctx, r); err == nil && st.restarts != before {
			t.Fatalf("traffic-manager restarted: restartCount %d, was %d", st.restarts, before)
		}
		time.Sleep(mgrapiPollEvery)
	}
	deadline = time.Now().Add(mgrapiReadyWait)
	for time.Now().Before(deadline) {
		st, err := mgrapiManagerStatus(ctx, r)
		if err == nil && st.restarts != before {
			t.Fatalf("traffic-manager restarted: restartCount %d, was %d", st.restarts, before)
		}
		if err == nil && st.ready {
			return
		}
		time.Sleep(mgrapiPollEvery)
	}
	t.Fatalf("traffic-manager pod is not Ready")
}

// mgrapiClientInfo is a valid ClientInfo for a session in ns.
func mgrapiClientInfo(r *rt.Runtime, name, ns string) *manager.ClientInfo {
	return &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: "rtest-" + name,
		Product:   "telepresence",
		Version:   r.Version().String(),
	}
}

// mgrapiAgentInfo is a valid AgentInfo for a pod that does not exist.
func mgrapiAgentInfo(ns, name, podUID string) *manager.AgentInfo {
	return &manager.AgentInfo{
		Name:      name,
		Namespace: ns,
		PodName:   name + "-pod",
		PodUid:    podUID,
		PodIp:     "10.255.255.1",
		Product:   "telepresence",
		Version:   "2.0.0",
		Mechanisms: []*manager.AgentInfo_Mechanism{{
			Name:    "tcp",
			Product: "telepresence",
			Version: "2.0.0",
		}},
	}
}
