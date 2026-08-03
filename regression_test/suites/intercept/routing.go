package intercept

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// InterceptRouting proves routing behavior across replica counts, multiple
// service ports, the localShortcut config on/off, and h2c preservation.
// Carries CompatCore: Test_MultiReplica's repeated requests to
// wl.ServiceURL() resolve through the manager's Lookup RPC (until a
// dedicated dns-area single-resolution test exists, per m4-spec section 2);
// see framework/compat/manifest.go.
type InterceptRouting struct {
	rt.Suite
}

func init() {
	rt.Register(&InterceptRouting{},
		rt.InArea("intercept"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

// multiReplicaRequests is how many sequential requests Test_MultiReplica
// sends: enough to very likely have hit every one of the workload's
// replicas at least once, were routing per-replica instead of
// intercept-wide.
const multiReplicaRequests = 20

// Test_MultiReplica proves a global intercept on a multi-replica workload
// routes every request to the local service, regardless of which replica
// would otherwise have served it (regression #4085).
func (s *InterceptRouting) Test_MultiReplica() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoReplicas("multi-echo", 4))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	url := wl.ServiceURL()
	for i := 0; i < multiReplicaRequests; i++ {
		rt.RoutedToLocal(t, url, ls)
	}
}

// Test_MultiPort proves an intercept on one named service port leaves the
// workload's other named port serving the cluster.
func (s *InterceptRouting) Test_MultiPort() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoMultiPort("multi-port"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	second, ok := wl.ServiceURLNamed("http2")
	if !ok {
		t.Fatalf("workload %s: no http2 port", wl.Name)
	}
	rt.RoutedToCluster(t, second)
}

// enableLocalShortcut and disableLocalShortcut are ConnWithConfig deltas for
// Test_LocalShortcut's config-variant connection pair. disableLocalShortcut
// matches the run's baseline (see Runtime.baselineConfig) but, going
// through ConnWithConfig, gets its own private connection fixture that
// nothing else in the run shares.
func enableLocalShortcut(c client.Config) {
	ic := c.Intercept()
	ic.LocalShortcut = true
	ic.LocalShortcutIsGlobal = true
}

func disableLocalShortcut(c client.Config) {
	ic := c.Intercept()
	ic.LocalShortcut = false
	ic.LocalShortcutIsGlobal = false
}

// Test_LocalShortcut proves intercept.localShortcut's effect on filtered
// intercepts: with it on and global, the root daemon routes every request
// from this host straight to the local handler, filter or not; with it off
// (the baseline), a non-matching request still reaches the cluster. Both
// connections go through rt.Mutate: they share the host's single daemon
// slot, so acquiring one quits and restarts the other under its own config
// dir (see fixture_connection.go's ensureHostConfigDir).
func (s *InterceptRouting) Test_LocalShortcut() {
	t := s.T()
	s.Manager()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("local-shortcut"))
	ls := s.LocalEcho()
	url := wl.ServiceURL()

	on := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(enableLocalShortcut)))
	a := on.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, headerVal))
	rt.RoutedToLocal(t, url, ls) // no header: the global shortcut takes it anyway
	a.Detach(t)

	off := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableLocalShortcut)))
	b := off.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, headerVal))
	defer b.Detach(t)
	rt.RoutedToLocal(t, url, ls, check.WithHeader(headerKey, headerVal))
	rt.RoutedToCluster(t, url)
}

// h2cMarkerPrefix begins the body every h2cServer responds with, mirroring
// rt.LocalService's marker convention (regression_test/framework/rt/
// fixture_localservice.go).
const h2cMarkerPrefix = "rtest-h2c:"

// h2cHeaderKey/h2cHeaderVal filters Test_H2C's intercept. An unfiltered
// (global) intercept uses mechanism "tcp" (pkg/client/cli/intercept/
// info.go's Info.Global: spec.Mechanism == "tcp"), a raw byte tunnel that
// would preserve any framing trivially and prove nothing about h2c
// handling. A header filter forces mechanism "http", the agent's HTTP-aware
// reverse proxy (cmd/traffic/cmd/agent/fwd/http.go's serveHTTPIntercept)
// that negotiates the workstation-bound connection's protocol from both the
// inbound request's own protocol and the service's appProtocol.
const (
	h2cHeaderKey = "x-rtest-h2c"
	h2cHeaderVal = "match"
)

// h2cRouteTimeout bounds Test_H2C's request poll.
const h2cRouteTimeout = 30 * time.Second

// h2cServer is a suite-local, h2c-only (prior-knowledge, cleartext HTTP/2)
// server: golang.org/x/net/http2.Server.ServeConn speaks HTTP/2 directly on
// every accepted connection, with no HTTP/1.1 fallback, so a response only
// arrives if whatever proxied the connection preserved HTTP/2 framing
// end-to-end rather than reinterpreting the bytes as HTTP/1.1. It is kept
// local to this suite rather than added to rt.LocalService, which is a
// plain HTTP/1.1 server.
type h2cServer struct {
	listener net.Listener
	marker   string
}

// newH2CServer starts an h2cServer and registers its shutdown as a test
// cleanup.
func newH2CServer(t testing.TB) *h2cServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("h2cServer: listen: %v", err)
	}
	srv := &h2cServer{listener: l, marker: h2cMarkerPrefix + randomH2CID()}
	h2s := &http2.Server{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(srv.marker))
	})
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed (t.Cleanup below)
			}
			go h2s.ServeConn(conn, &http2.ServeConnOpts{Handler: handler})
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return srv
}

// Port returns the local TCP port the server is bound to.
func (s *h2cServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // always tcp, see net.Listen above
}

func randomH2CID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newH2CClient returns an http.Client that issues prior-knowledge,
// cleartext HTTP/2 requests: no TLS, no HTTP/1.1 upgrade. AllowHTTP plus a
// DialTLSContext that dials a plain (non-TLS) connection is the documented
// way to get this from golang.org/x/net/http2.Transport; net/http's own
// client transport only ever speaks h2c when negotiated over TLS ALPN,
// which prior-knowledge h2c (kubernetes.io/h2c) does not use.
func newH2CClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// Test_H2C proves that a real h2c (HTTP/2 prior-knowledge, cleartext)
// request to a header-filtered intercept reaches a local h2c-only server.
// The service port declares appProtocol: kubernetes.io/h2c
// (workloads.Template.AppProtocol), and the probe itself uses genuine HTTP/2
// prior knowledge (newH2CClient); an h2c-only local server only answers if
// the agent's reverse proxy preserved h2c framing all the way to the
// workstation instead of downgrading to HTTP/1.1.
func (s *InterceptRouting) Test_H2C() {
	t := s.T()
	conn := s.Connect()
	tpl := workloads.Echo("h2c-intercept")
	tpl.AppProtocol = "kubernetes.io/h2c"
	wl := s.Workload(tpl)
	srv := newH2CServer(t)

	a := conn.Intercept(t, wl,
		cli.Port(srv.Port(), "http"), cli.MountFalse(), cli.HTTPHeader(h2cHeaderKey, h2cHeaderVal))
	defer a.Detach(t)

	client := newH2CClient()
	url := wl.ServiceURL()
	s.Eventually(func() bool {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		req.Header.Set(h2cHeaderKey, h2cHeaderVal)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return resp.StatusCode == http.StatusOK && strings.Contains(string(body), srv.marker)
	}, h2cRouteTimeout, 250*time.Millisecond, "h2c request should reach the local h2c server")
}
