package dns

import (
	"fmt"
	"net"
	"strconv"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// dnsMappingAlias is the alias name Test_Mappings adds via a dns.mappings
// kubeconfig extension entry.
const dnsMappingAlias = "dns-mappings-alias"

// ExcludesMappings proves the kubeconfig telepresence.io extension's
// dns.excludes and dns.mappings reach a live session: an excluded name never
// resolves, and a mapped alias resolves to (and serves) the name it
// aliases. Mirrors integration_test/uhn_dns_test.go's Test_UHNExcludes/
// Test_UHNMappings semantics, narrowed to one excluded/mapped name each.
// Each test connects fresh under its own derived kubeconfig (rt.Mutate +
// rt.ConnWithKubeconfig) and disconnects before returning, so no area
// running after this one adopts a connection scoped to these extensions.
type ExcludesMappings struct {
	rt.Suite
}

func init() {
	rt.Register(&ExcludesMappings{}, rt.InArea("dns"), rt.NeedsManager(managers.Default))
}

// Test_Excludes connects with dns.excludes hiding the echo service's
// single-label name -- the same form Resolution.Test_ServiceNameForms proves
// resolves when unexcluded -- and checks the name never resolves.
func (s *ExcludesMappings) Test_Excludes() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("dns-excludes-echo"))

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"dns": map[string]any{"excludes": []string{wl.SvcName}},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithKubeconfig(path)))
	t.Cleanup(func() { conn.Disconnect(t) })

	s.Eventually(func() bool {
		return !lookupSucceeds(ctx, wl.SvcName)
	}, lookupPollTimeout, lookupPollInterval, "%s unexpectedly resolved while excluded", wl.SvcName)
}

// Test_Mappings connects with dns.mappings adding an alias name that
// resolves to the echo service, and checks the alias resolves and serves
// the echo response.
func (s *ExcludesMappings) Test_Mappings() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("dns-mappings-echo"))

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"dns": map[string]any{"mappings": []map[string]string{
			{"name": dnsMappingAlias, "aliasFor": fmt.Sprintf("%s.%s", wl.SvcName, wl.Namespace)},
		}},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithKubeconfig(path)))
	t.Cleanup(func() { conn.Disconnect(t) })

	rt.RoutedToCluster(t, "http://"+net.JoinHostPort(dnsMappingAlias, strconv.Itoa(wl.Port)))
}
