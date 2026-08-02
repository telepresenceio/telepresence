package install

import (
	"slices"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// podCIDRA/podCIDRB are RFC 5737 documentation ranges (TEST-NET-1/TEST-NET-3):
// safe, non-routable subnets that won't collide with a real cluster or host
// route, used only to prove an explicit podCIDRs list reaches a connection's
// reported subnets.
const (
	podCIDRA = "192.0.2.0/24"
	podCIDRB = "203.0.113.0/24"
)

// subnetPollTimeout/subnetPollInterval bound how long a fresh connection's
// status takes to reflect the release's podCIDRs after a reconnect.
const (
	subnetPollTimeout  = 30 * time.Second
	subnetPollInterval = 2 * time.Second
)

// rootDaemonSubnets parses the field of `status --format json`'s root_daemon
// object that cli.Status's RootDaemonStatus doesn't mirror (pkg/client's
// server-side RootDaemonStatus): the routed subnet list.
type rootDaemonSubnets struct {
	RootDaemon struct {
		Subnets []string `json:"subnets"`
	} `json:"root_daemon"`
}

// PodCIDRs proves that podCIDRStrategy=environment plus an explicit podCIDRs
// list on the shared release reaches a fresh connection's reported subnets.
type PodCIDRs struct {
	rt.Suite
}

func init() {
	rt.Register(&PodCIDRs{}, rt.InArea("install"))
}

// Test_ExplicitCIDRsReachStatus switches the shared release to
// podCIDRStrategy=environment with two explicit CIDRs, reconnects, and
// checks that `status`'s root daemon subnets contain both.
func (s *PodCIDRs) Test_ExplicitCIDRsReachStatus() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	// Free whatever connection is currently memoized while the release
	// still runs its previous spec: a session's cluster-info (subnets
	// included) is read at connect time, so the release must switch before
	// reconnecting.
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)

	spec := managers.Spec{
		Key: "pod-cidrs",
		Values: managers.Values{
			PodCIDRs:        []string{podCIDRA, podCIDRB},
			PodCIDRStrategy: "environment",
		},
	}
	rt.Mutate(t, rt.ManagerFixture(spec))
	rt.Reconnect(t, ctx, ns)

	s.Eventually(func() bool {
		var st rootDaemonSubnets
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		return slices.Contains(st.RootDaemon.Subnets, podCIDRA) && slices.Contains(st.RootDaemon.Subnets, podCIDRB)
	}, subnetPollTimeout, subnetPollInterval, "root daemon subnets should include the explicit podCIDRs")
}
