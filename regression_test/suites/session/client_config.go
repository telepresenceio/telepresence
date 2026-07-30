package session

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// clientConfigSuffix/clientConfigAlsoProxy are the distinctive
// dns.includeSuffixes and routing.alsoProxySubnets values the shared
// release serves for this suite's spec, chosen so they cannot collide with
// anything the cluster itself would ever discover: clientConfigAlsoProxy is
// a TEST-NET-3 (RFC 5737) documentation range. An also-proxy is the served
// routing value with an observable effect (it ADDS a routed subnet); a
// served never-proxy of a non-cluster CIDR changes nothing visible.
const (
	clientConfigSuffix    = "rtest-clientconfig.internal"
	clientConfigAlsoProxy = "203.0.113.0/24"
)

// clientConfigSpec is the manager spec ClientConfig declares: the cluster
// serves a distinctive dns.includeSuffixes entry and a
// routing.alsoProxySubnets entry.
//
//nolint:gochecknoglobals // catalog spec, referenced by both Register and the test
var clientConfigSpec = managers.ClientConfig("dns-routing", managers.Values{
	Client: managers.Client{
		DNS:     managers.ClientDNS{IncludeSuffixes: []string{clientConfigSuffix}},
		Routing: managers.ClientRouting{AlsoProxySubnets: []string{clientConfigAlsoProxy}},
	},
})

// ClientConfig proves the shared release's cluster-served client.* config
// (cloud_config_test.go's core) reaches a fresh connection: ported onto
// `status --format json`'s root_daemon fields instead of the text "Never
// Proxy" count cloud_config_test.go polled for, since this framework's
// workloads give no distinctive live IP to make an actual routing probe
// meaningful.
type ClientConfig struct {
	rt.Suite
}

func init() {
	rt.Register(&ClientConfig{}, rt.InArea("session"), rt.NeedsManager(clientConfigSpec))
}

// Test_ServedConfigReflectsOnConnect forces a genuinely fresh connection
// (freeDefaultConnection + rt.Reconnect, not Suite.Connect: the default
// connection fixture may already be memoized from a connection made before
// this suite's manager spec went live, and a manager spec switch alone never
// invalidates it) and checks that `status --format json` reflects both
// served values.
func (s *ClientConfig) Test_ServedConfigReflectsOnConnect() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	freeDefaultConnection(t, ns)
	conn := rt.Reconnect(t, ctx, ns)
	defer conn.Disconnect(t)

	var st daemonStatus
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))

	s.Contains(st.RootDaemon.DNS.IncludeSuffixes, clientConfigSuffix,
		"served dns.includeSuffixes should reach the connecting client")
	s.Contains(st.RootDaemon.AlsoProxy, clientConfigAlsoProxy,
		"served routing.alsoProxySubnets should reach the connecting client")
}
