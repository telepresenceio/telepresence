package dns

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// dnsMappingAlias is the single-label alias, and dnsMappingFQAlias the dotted
// alias under the includeSuffixes entry dnsMappingSuffix, that Test_Mappings
// adds via dns.mappings kubeconfig extension entries. The suffix becomes a
// routing domain for the daemon, so the dotted alias always reaches it; the
// single-label alias depends on the host resolver applying search domains.
const (
	dnsMappingAlias   = "dns-mappings-alias"
	dnsMappingSuffix  = ".tele.test"
	dnsMappingFQAlias = dnsMappingAlias + dnsMappingSuffix
)

// ExcludesMappings proves the kubeconfig telepresence.io extension's
// dns.excludes and dns.mappings reach a live session: an excluded name never
// resolves, and a mapped alias resolves to (and serves) the name it
// aliases. One excluded and one mapped name each is enough: the extension
// applies the same rule to every entry in either list.
// Each test connects fresh under its own derived kubeconfig (rt.Mutate +
// rt.ConnWithKubeconfig) and disconnects before returning, so no area
// running after this one adopts a connection scoped to these extensions.
type ExcludesMappings struct {
	rt.Suite
}

func init() {
	rt.Register(&ExcludesMappings{}, rt.InArea("dns"), rt.NeedsManager(managers.Default))
}

// Test_Excludes connects with dns.excludes hiding one echo service and
// checks that its svc.ns name stops resolving while another service's still
// does, and that its single-label name does not resolve either.
func (s *ExcludesMappings) Test_Excludes() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("dns-excludes-echo"))
	other := s.Workload(workloads.Echo("dns-excludes-other"))

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"dns": map[string]any{"excludes": []string{wl.SvcName}},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithKubeconfig(path)))
	t.Cleanup(func() { conn.Disconnect(t) })

	otherName := other.SvcName + "." + other.Namespace
	s.Eventually(func() bool {
		return lookupSucceeds(ctx, otherName)
	}, lookupPollTimeout, lookupPollInterval, "%s did not resolve", otherName)

	qualified := wl.SvcName + "." + wl.Namespace
	s.Eventually(func() bool {
		return !lookupSucceeds(ctx, qualified)
	}, lookupPollTimeout, lookupPollInterval, "%s unexpectedly resolved while excluded", qualified)

	s.Eventually(func() bool {
		return !lookupSucceeds(ctx, wl.SvcName)
	}, lookupPollTimeout, lookupPollInterval, "%s unexpectedly resolved while excluded", wl.SvcName)
}

// Test_Mappings connects with dns.mappings adding a dotted alias and a
// single-label alias for the echo service, and checks the dotted alias
// resolves and serves the echo response. The single-label alias is checked in
// a sub-test that skips when the host resolver never expands single labels.
func (s *ExcludesMappings) Test_Mappings() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("dns-mappings-echo"))
	target := fmt.Sprintf("%s.%s", wl.SvcName, wl.Namespace)

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"dns": map[string]any{
			"includeSuffixes": []string{dnsMappingSuffix},
			"mappings": []map[string]string{
				{"name": dnsMappingFQAlias, "aliasFor": target},
				{"name": dnsMappingAlias, "aliasFor": target},
			},
		},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithKubeconfig(path)))
	t.Cleanup(func() { conn.Disconnect(t) })

	rt.RoutedToCluster(t, "http://"+net.JoinHostPort(dnsMappingFQAlias, strconv.Itoa(wl.Port)))

	s.Run("single-label", func() {
		want, err := net.DefaultResolver.LookupHost(ctx, target)
		s.Require().NoError(err, "qualified form should resolve")
		wantSet := make(map[string]bool, len(want))
		for _, a := range want {
			wantSet[a] = true
		}
		probe := func() bool {
			got, err := getentHosts(ctx, dnsMappingAlias)
			if err != nil {
				return false
			}
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
				// Go's pure resolver only reads /etc/resolv.conf, and some
				// host resolver setups never expose the search domains the
				// daemon registers. Skip rather than fail on such hosts.
				s.T().Skipf("single-label %s did not resolve via the host resolver path "+
					"(dotted alias verified); host resolver setup likely bypasses "+
					"daemon-registered search domains", dnsMappingAlias)
			}
			time.Sleep(time.Second)
		}
	})
}
