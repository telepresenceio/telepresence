package session

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// daemonStatus mirrors the subset of `telepresence status --format json`'s
// root_daemon object this area asserts on: the fields
// pkg/client/cli/cmd/status.go's RootDaemonStatus promotes from its embedded
// *client.RoutingSnake (subnets, never_proxy_subnets) plus its named DNS
// field (dns.include_suffixes). regression_test/framework/cli.Status only
// mirrors the top-level running/version fields, not these, so this area
// keeps its own narrower mirror instead of widening the shared one.
type daemonStatus struct {
	RootDaemon struct {
		DNS struct {
			IncludeSuffixes []string `json:"include_suffixes"`
		} `json:"dns"`
		Subnets           []string `json:"subnets"`
		AlsoProxy         []string `json:"also_proxy_subnets"`
		NeverProxySubnets []string `json:"never_proxy_subnets"`
	} `json:"root_daemon"`
}

// subnetContainsPrefix reports whether target equals, or falls within, any
// of subnets (given as their canonical netip.Prefix string form, as
// `status --format json` renders them). Unparsable entries are ignored.
func subnetContainsPrefix(subnets []string, target netip.Prefix) bool {
	for _, raw := range subnets {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			continue
		}
		if p == target || p.Contains(target.Addr()) {
			return true
		}
	}
	return false
}

// freeDefaultConnection claims the shared default connection fixture (so the
// engine re-provisions it fresh afterwards) and quits whatever daemon is
// running under that identity. Mirrors suites/connect/lifecycle.go's and
// suites/namespaces/helpers.go's identically named, package-private helper:
// a test about to reconnect under a different manager spec or client config
// must call this first, since switching the manager fixture alone never
// invalidates an already-memoized connection fixture (they are separate memo
// entries).
func freeDefaultConnection(t *testing.T, ns string) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
}

// arriveAsClient establishes a raw manager session in ns, independent of any
// `telepresence connect` session, for suites that talk to the manager
// directly via rt.ManagerClient (WorkloadWatch, ManagerInfo): a background
// goroutine calls Remain every 5s to keep the session alive and Departs once
// ctx is done. Callers pass a context.WithCancel they cancel via defer, well
// before the suite's own context ends.
func arriveAsClient(
	t testing.TB, ctx context.Context, mc manager.ManagerClient, name, ns string,
) *manager.SessionInfo {
	t.Helper()
	si, err := mc.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: "rtest-" + name,
		Product:   "telepresence",
		Version:   rt.R().Version().String(),
	})
	if err != nil {
		t.Fatalf("ArriveAsClient: %v", err)
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = mc.Remain(context.Background(), &manager.RemainRequest{Session: si})
			case <-ctx.Done():
				_, _ = mc.Depart(context.Background(), si)
				return
			}
		}
	}()
	return si
}
