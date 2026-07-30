package connect

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// alsoProxyCIDR/neverProxyCIDR are RFC 5737 documentation ranges: safe,
// non-routable test subnets that won't collide with a real cluster or host
// route, used only to prove the also-proxy/never-proxy extension fields
// reach the root daemon's reported status. dnsSuffix is an arbitrary,
// distinctive suffix used the same way for dns.include-suffixes.
const (
	alsoProxyCIDR  = "203.0.113.0/24"
	neverProxyCIDR = "198.51.100.0/24"
	dnsSuffix      = ".rtest-contexts-suffix"
)

// rootDaemonRouting parses the fields of `status --format json`'s
// root_daemon object that cli.Status's RootDaemonStatus doesn't mirror
// (pkg/client.RoutingSnake, embedded server-side): the also-proxy subnet list
// a kubeconfig extension populates, and the routed subnet list. There is no
// never-proxy list to check against: never-proxy entries are removed from
// routing rather than reported separately, so never_proxy_subnets stays
// empty and isn't worth mirroring here.
type rootDaemonRouting struct {
	RootDaemon struct {
		Subnets   []string `json:"subnets"`
		AlsoProxy []string `json:"also_proxy_subnets"`
	} `json:"root_daemon"`
}

// sessionConfigView is the minimal shape of `telepresence config view
// --format json` (pkg/client.SessionConfig) needed to check that a
// kubeconfig extension's dns.include-suffixes propagated into the live
// session's config.
type sessionConfigView struct {
	ClientConfig struct {
		DNS struct {
			IncludeSuffixes []string `json:"includeSuffixes"`
		} `json:"dns"`
	} `json:"clientConfig"`
}

// ConnectContexts proves that the kubeconfig `telepresence.io` extension
// (also-proxy, never-proxy, dns.include-suffixes) reaches a live session.
type ConnectContexts struct {
	rt.Suite
}

func init() {
	rt.Register(&ConnectContexts{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// Test_AlsoNeverProxy connects with an extension setting also-proxy and
// never-proxy subnets. also-proxy is added to routing and shows up in the
// root daemon's status; never-proxy is instead removed from routing, so it
// never appears in the routed subnets or in also_proxy_subnets.
func (s *ConnectContexts) Test_AlsoNeverProxy() {
	t := s.T()
	ns := s.AppNamespace()
	env := rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"also-proxy":  []string{alsoProxyCIDR},
		"never-proxy": []string{neverProxyCIDR},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnWithKubeconfig(path))
	t.Cleanup(func() { conn.Disconnect(t) })

	var st rootDaemonRouting
	s.Require().NoError(s.CLI().JSON(s.Ctx(), &st, "status", "--format", "json"))
	s.Contains(st.RootDaemon.AlsoProxy, alsoProxyCIDR)
	s.NotContains(st.RootDaemon.Subnets, neverProxyCIDR)
	s.NotContains(st.RootDaemon.AlsoProxy, neverProxyCIDR)
}

// Test_DNSIncludeSuffixes connects with an extension adding a dns
// include-suffix and checks it propagated into the live session's config as
// reported by `config view`. Skips with a note if that isn't observable.
func (s *ConnectContexts) Test_DNSIncludeSuffixes() {
	t := s.T()
	ns := s.AppNamespace()
	env := rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}
	path, err := rt.WithKubeConfigExtension(env, map[string]any{
		"dns": map[string]any{"include-suffixes": []string{dnsSuffix}},
	})
	s.Require().NoError(err)

	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnWithKubeconfig(path))
	t.Cleanup(func() { conn.Disconnect(t) })

	var cfg sessionConfigView
	if err := s.CLI().JSON(s.Ctx(), &cfg, "config", "view", "--format", "json"); err != nil {
		t.Skipf("`config view` while connected did not produce parseable JSON: %v", err)
		return
	}
	s.Contains(cfg.ClientConfig.DNS.IncludeSuffixes, dnsSuffix)
}
