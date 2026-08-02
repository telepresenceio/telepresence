package routing

import (
	"context"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// daemonStatus mirrors the subset of `telepresence status --format json`'s
// root_daemon object this area asserts on: the fields
// pkg/client/cli/cmd/status.go's RootDaemonStatus promotes from its embedded
// *client.RoutingSnake (subnets, never_proxy_subnets, virtual_subnet).
// regression_test/framework/cli.Status only mirrors the top-level
// running/version fields, not these, so this area keeps its own narrower
// mirror instead of widening the shared one (mirrors
// suites/install/pod_cidrs.go's and suites/session/helpers.go's
// identically-shaped, package-private types).
type daemonStatus struct {
	RootDaemon struct {
		Subnets           []string `json:"subnets"`
		NeverProxySubnets []string `json:"never_proxy_subnets"`
		VirtualSubnet     string   `json:"virtual_subnet"`
	} `json:"root_daemon"`
}

// subnetPollTimeout/subnetPollInterval bound how long a fresh connection's
// status takes to settle on its routed subnets: `connect` returns once the
// user daemon is up, before the root daemon's cluster-info watch has
// necessarily finished reconciling routes (mirrors
// suites/install/pod_cidrs.go's identically valued constants).
const (
	subnetPollTimeout  = 30 * time.Second
	subnetPollInterval = 2 * time.Second
)

// awaitRouted polls status until the root daemon reports at least one
// routed subnet. No reconnect fallback: a reconnect would drop the
// connection's flags (--proxy-via, --allow-conflicting-subnets). The window
// covers on-demand agent injection (~15-20s) plus route setup; the
// connect-time daemon race is already retried by the connection fixture.
func awaitRouted(t *testing.T, ctx context.Context, tp *cli.TP, _ string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		var st daemonStatus
		if err := tp.JSON(ctx, &st, "status", "--format", "json"); err == nil && len(st.RootDaemon.Subnets) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("root daemon reported no routed subnets within 90s")
		}
		time.Sleep(subnetPollInterval)
	}
}

// routedWithin reports whether status shows any routed subnet within d.
func routedWithin(ctx context.Context, tp *cli.TP, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var st daemonStatus
		if err := tp.JSON(ctx, &st, "status", "--format", "json"); err == nil && len(st.RootDaemon.Subnets) > 0 {
			return true
		}
		time.Sleep(subnetPollInterval)
	}
	return false
}
