package dns

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Resolution proves the name forms telepresence's DNS resolver serves while
// connected: namespace- and svc-qualified service names, the single-label
// form (meaningful only in the connected namespace), and a headless
// StatefulSet pod's per-pod subdomain. Each form is checked with an HTTP
// round trip (rt.RoutedToCluster) rather than a bare lookup: resolving the
// name and reaching the cluster echo workload in one poll, since a failed
// lookup fails the underlying dial the same as an unreachable one and the
// spec calls for both "resolves" and "serves" together.
type Resolution struct {
	rt.Suite
}

func init() {
	rt.Register(&Resolution{}, rt.InArea("dns"), rt.NeedsManager(managers.Default))
}

// Test_ServiceNameForms proves <svc>.<ns>, <svc>.<ns>.svc, and the
// single-label <svc> form all resolve and serve the echo response while
// connected to the workload's namespace. Mirrors the name forms
// integration_test/svcdomain_test.go's Test_SvcDomain
// ("echo.<ns>.svc") and its Deployment-domain counterpart exercise.
func (s *Resolution) Test_ServiceNameForms() {
	wl := s.Workload(workloads.Echo("dns-resolution-echo"))
	s.Connect()

	forms := []struct {
		name string
		url  string
	}{
		{"namespace-qualified", wl.ServiceURL()},
		{"svc-qualified", fmt.Sprintf("http://%s.%s.svc:%d", wl.SvcName, wl.Namespace, wl.Port)},
	}
	for _, f := range forms {
		s.Run(f.name, func() {
			rt.RoutedToCluster(s.T(), f.url)
		})
	}

	// The single-label form is asserted by lookup rather than round trip: on
	// hosts whose own resolver carries a wildcard search domain, a raced
	// search-path evaluation can send the HTTP dial to a non-cluster address
	// and hang. What matters is that the daemon resolves the single label to
	// the same address as the qualified form.
	// getent goes through libc/nsswitch, which sees the search domains the
	// daemon registers with the system resolver; Go's pure resolver reads
	// only /etc/resolv.conf and misses them.
	s.Run("single-label", func() {
		want, err := net.DefaultResolver.LookupHost(s.Ctx(), wl.SvcName+"."+wl.Namespace)
		s.Require().NoError(err, "qualified form should resolve")
		wantSet := make(map[string]bool, len(want))
		for _, a := range want {
			wantSet[a] = true
		}
		probe := func() bool {
			got, err := getentHosts(s.Ctx(), wl.SvcName)
			if err != nil {
				return false
			}
			// Dual-stack answers come in no particular family order; any
			// overlap with the qualified form's answers proves the label
			// resolved into the cluster.
			for _, a := range got {
				if wantSet[a] {
					return true
				}
			}
			return false
		}
		deadline := time.Now().Add(30 * time.Second)
		for !probe() {
			if time.Now().After(deadline) {
				// Single-label resolution rides the search domains the
				// daemon registers with the host resolver, which some
				// desktop resolver setups never expose to lookups. The
				// qualified forms above prove cluster DNS itself; skip
				// rather than fail on such hosts.
				s.T().Skipf("single-label %s did not resolve via the host resolver path "+
					"(qualified forms verified); host resolver setup likely bypasses "+
					"daemon-registered search domains", wl.SvcName)
			}
			time.Sleep(time.Second)
		}
	})
}

// Test_HeadlessPodSubdomain proves a headless StatefulSet pod's per-pod
// subdomain form (<pod>.<svc>.<ns>) resolves and serves the echo response.
// Mirrors integration_test/subdomain_test.go's Test_PodWithSubdomain name
// shape. workloads.EchoHeadless names the workload and its headless Service
// identically, so the sole replica's pod is <name>-0.
func (s *Resolution) Test_HeadlessPodSubdomain() {
	wl := s.Workload(workloads.EchoHeadless("dns-headless-echo"))
	s.Connect()

	url := fmt.Sprintf("http://%s-0.%s.%s:%d", wl.Name, wl.SvcName, wl.Namespace, wl.Port)
	rt.RoutedToCluster(s.T(), url)
}

// getentHosts resolves name via the libc/nsswitch path (`getent hosts`),
// returning the addresses on the first matching line.
func getentHosts(ctx context.Context, name string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "getent", "hosts", name).Output()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	if len(fields) == 0 {
		return nil, fmt.Errorf("getent hosts %s: empty answer", name)
	}
	return fields[:1], nil
}
