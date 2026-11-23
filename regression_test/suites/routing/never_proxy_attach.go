package routing

import (
	"net/netip"
	"slices"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// NeverProxyAttach proves that a workload whose pod address is excluded by
// --never-proxy cannot be attached to: intercept and ingest fail with a
// message naming the address and the never-proxy subnet, and leave nothing
// registered.
type NeverProxyAttach struct {
	rt.Suite
}

func init() {
	rt.Register(&NeverProxyAttach{}, rt.InArea("routing"), rt.NeedsManager(managers.Default))
}

type podList struct {
	Items []struct {
		Metadata struct {
			DeletionTimestamp string `json:"deletionTimestamp"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
			PodIP string `json:"podIP"`
		} `json:"status"`
	} `json:"items"`
}

// Test_PodAddressNeverProxied attaches once on a plain connection, which
// leaves the traffic-agent in place so that the pod keeps its address, and
// then reconnects with --never-proxy <pod IP>/32. A replace is not covered:
// it re-creates the pod, so the address that was excluded is not the one
// the workload ends up with.
func (s *NeverProxyAttach) Test_PodAddressNeverProxied() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	wl := s.Workload(workloads.Echo("never-proxy-attach"))
	ls := s.LocalEcho()

	// Control: both attachments work without the never-proxy rule.
	conn := s.Connect()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	a.Detach(t)
	a = conn.Ingest(t, wl, cli.MountFalse())
	a.Detach(t)

	var podIP netip.Addr
	s.Eventually(func() bool {
		var pods podList
		if err := s.R().KubectlJSON(ctx, ns, &pods, "get", "pods", "-l", "app="+wl.Name); err != nil {
			return false
		}
		var ips []string
		for _, p := range pods.Items {
			if p.Metadata.DeletionTimestamp == "" && p.Status.Phase == "Running" && p.Status.PodIP != "" {
				ips = append(ips, p.Status.PodIP)
			}
		}
		if len(ips) != 1 {
			return false
		}
		var err error
		podIP, err = netip.ParseAddr(ips[0])
		return err == nil
	}, subnetPollTimeout, subnetPollInterval, "workload should have exactly one running pod")
	npx := netip.PrefixFrom(podIP, podIP.BitLen()).String()

	conn = rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--never-proxy", npx)))
	defer conn.Disconnect(t)

	s.Eventually(func() bool {
		var st daemonStatus
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		return slices.Contains(st.RootDaemon.NeverProxySubnets, npx)
	}, subnetPollTimeout, subnetPollInterval,
		"never-proxy %s should be in effect (the pod subnet must be routed)", npx)

	for _, verb := range []string{"intercept", "ingest"} {
		args := []string{verb, wl.Name, "--namespace", ns, "--mount", "false"}
		if verb == "intercept" {
			args = append(args, rt.ToLocal(ls, "http")()...)
		}
		stdout, stderr, err := s.CLI().Run(ctx, args...)
		s.Require().Error(err, "%s of a never-proxied pod must fail\nstdout:\n%s\nstderr:\n%s", verb, stdout, stderr)
		out := stdout + stderr
		s.Contains(out, "excluded by never-proxy subnet "+npx, "%s output", verb)
		s.Contains(out, podIP.String(), "%s output", verb)
	}

	s.Eventually(func() bool {
		for _, e := range conn.List(t) {
			if e.Name == wl.Name && (len(e.InterceptInfo) > 0 || len(e.IngestInfo) > 0) {
				return false
			}
		}
		return true
	}, subnetPollTimeout, subnetPollInterval, "no attachment may remain after a refused attach")
}
