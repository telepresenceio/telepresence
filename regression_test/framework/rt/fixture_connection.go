package rt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// Conn is a live `telepresence connect` session.
type Conn struct {
	r         *Runtime
	ctx       context.Context
	namespace string
	name      string // set by ConnNamed; "" for the default, unnamed connection
	docker    bool   // set by ConnDocker
}

// Attach is a live intercept, ingest, replace, or wiretap.
type Attach struct {
	conn      *Conn
	namespace string
	name      string
	Intercept *cli.InterceptInfo
	Ingest    *cli.IngestInfo
	Replace   *cli.InterceptInfo
	Wiretap   *cli.InterceptInfo
}

// connectAs is the --as identity connections use: the test ServiceAccount
// managerRbac/clientRbac grant access to. Its ClusterRoleBinding is
// cluster-scoped, so the same identity works against any manager release
// (the shared one or a SecondaryManager), regardless of namespace.
const connectAs = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount

// connSpec accumulates the effect of ConnOpts applied to one connection: the
// `connect` invocation's extra arguments and environment, and the metadata
// (name, docker mode, config dir) the fixture and the resulting Conn need.
type connSpec struct {
	args      []string
	env       []string
	name      string
	docker    bool
	configDir string
	hashKey   []string
	configErr error
}

// isDefault reports whether no ConnOpt changed anything: the plain
// `Connect()` case, whose fixture hash and adoption behavior must match the
// pre-M3 framework exactly.
func (cs *connSpec) isDefault() bool {
	return cs.name == "" && !cs.docker && cs.configDir == "" && len(cs.env) == 0
}

func newConnSpec(opts []ConnOpt) *connSpec {
	cs := &connSpec{}
	for _, o := range opts {
		o(cs)
	}
	return cs
}

// ConnOpt configures a connection beyond the base `telepresence connect`
// invocation: extra command-line arguments, environment overrides for that
// invocation, and metadata (name, docker mode, config dir) the fixture and
// the resulting Conn need afterwards. Every option that changes connection
// identity also extends the fixture hash, so distinct configurations never
// share a memoized connection.
type ConnOpt func(*connSpec)

// ConnNamed sets --name on the connect invocation. The resulting Conn
// carries the name and passes --use <name> on every subsequent
// per-connection CLI call, so it (and only it) is addressed even when other
// connections are live.
func ConnNamed(name string) ConnOpt {
	return func(cs *connSpec) {
		cs.name = name
		cs.args = append(cs.args, "--name", name)
		cs.hashKey = append(cs.hashKey, "name="+name)
	}
}

// ConnDocker sets --docker: the connection runs through a containerized
// daemon, independent of the host daemon (and of other docker connections).
func ConnDocker() ConnOpt {
	return func(cs *connSpec) {
		cs.docker = true
		cs.args = append(cs.args, "--docker")
		cs.hashKey = append(cs.hashKey, "docker=true")
	}
}

// ConnWithKubeconfig sets KUBECONFIG=path for this connect invocation only.
// `connect` has no --kubeconfig flag; the daemon reads KUBECONFIG from its
// environment instead. Build path with KubeConfigCopy/WithKubeConfigExtension.
func ConnWithKubeconfig(path string) ConnOpt {
	return func(cs *connSpec) {
		cs.env = append(cs.env, "KUBECONFIG="+path)
		cs.hashKey = append(cs.hashKey, "kubeconfig="+path)
	}
}

// ConnWithConfig runs the connection's daemon under a client config
// variant: the run's baseline config (Runtime.baselineConfig) with delta
// applied on top. The resulting config's content fingerprints the fixture
// hash. Only one such daemon runs on the host at a time: provisioning a
// connection whose config dir differs from the currently running host
// daemon's quits that daemon first (see ensureHostConfigDir); docker-mode
// connections are unaffected, since each runs its own containerized daemon.
func ConnWithConfig(delta func(client.Config)) ConnOpt {
	return func(cs *connSpec) {
		dir, fingerprint, err := R().variantConfigDir(delta)
		if err != nil {
			cs.configErr = err
			return
		}
		cs.configDir = dir
		cs.hashKey = append(cs.hashKey, "config="+fingerprint)
	}
}

// activeHostConfigDir is the config dir the currently running host
// (non-docker) daemon was started with; seeded to the baseline config dir
// once newRuntime's writeBaselineConfig/quitOnConfigChange has reconciled
// any stale daemon at startup. Guarded by the engine's serial execution
// (M1): suites run one at a time, so no lock is needed.
//
// ensureHostDaemon quits any running host daemon before a host connection
// is provisioned. Provisioning means "make it fresh": a running daemon may
// hold a different configuration (config dir, KUBECONFIG, namespace) or a
// degraded session — `connect` against it would silently reuse both. The
// healthy-daemon fast path is adoption, which never reaches this. Docker
// daemons are containerized and independent, so this is a no-op for them.
//
//nolint:gochecknoglobals // single, serial test run; see M1 contract
func ensureHostDaemon(e Env, cs *connSpec) {
	if cs.docker {
		// A containerized daemon left over from an earlier run would be
		// silently reused by name; quit its session so the connect starts
		// fresh. No -s: that flag stops ALL daemons and ignores --use.
		if cs.name != "" {
			if _, stderr, err := e.R.CLI().Run(e.Ctx, "quit", "--use", cs.name); err != nil {
				e.R.Infof("[rtest] quit before docker connect %s: %v: %s", cs.name, err, stderr)
			}
		}
		return
	}
	if _, stderr, err := e.R.CLI().Run(e.Ctx, "quit", "-s"); err != nil {
		e.R.Infof("[rtest] quit before host connect: %v: %s", err, stderr)
	}
}

func connectArgs(ns string, cs *connSpec) []string {
	args := make([]string, 0, 7+len(cs.args))
	args = append(args,
		"connect",
		"--namespace", ns,
		"--manager-namespace", managers.ManagerNamespace,
		"--as", connectAs,
	)
	return append(args, cs.args...)
}

func connectionHash(args, extra []string) string {
	parts := append(append([]string{}, args...), extra...)
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// ConnectionFixture is a `telepresence connect` session to ns, keyed by
// (namespace, opts). Owns quit-on-teardown.
func ConnectionFixture(ns string, opts ...ConnOpt) *Fixture[*Conn] {
	cs := newConnSpec(opts)
	args := connectArgs(ns, cs)
	hash := connectionHash(args, cs.hashKey)
	name := "connection/" + ns
	if cs.name != "" {
		name += "/" + cs.name
	}
	return &Fixture[*Conn]{
		Name: name,
		Hash: hash,
		ProvisionFn: func(e Env) (*Conn, error) {
			if cs.configErr != nil {
				return nil, cs.configErr
			}
			return provisionConnection(e, ns, args, cs)
		},
		AdoptFn: func(e Env) (*Conn, bool) {
			if !cs.isDefault() {
				return nil, false
			}
			return adoptConnection(e, ns)
		},
		DestroyFn: destroyConnection,
	}
}

func provisionConnection(e Env, ns string, args []string, cs *connSpec) (*Conn, error) {
	dir := cs.configDir
	if dir == "" {
		dir = e.R.configDir
	}
	ensureHostDaemon(e, cs)

	overrides := map[string]string{"DEV_TELEPRESENCE_CONFIG_DIR": dir}
	for _, kv := range cs.env {
		k, v, _ := strings.Cut(kv, "=")
		overrides[k] = v
	}
	stdout, stderr, err := e.R.CLIWithEnv(overrides).Run(e.Ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("connect: %w: %s", err, stderr)
	}
	if s := strings.TrimSpace(stdout); s != "" {
		e.R.Infof("[rtest] connect %s: %s", ns, s)
	}
	return &Conn{r: e.R, ctx: e.Ctx, namespace: ns, name: cs.name, docker: cs.docker}, nil
}

func adoptConnection(e Env, ns string) (*Conn, bool) {
	var st cli.Status
	if err := e.R.CLI().JSON(e.Ctx, &st, "status", "--format", "json"); err != nil {
		return nil, false
	}
	if !st.UserDaemon.Running || st.UserDaemon.Namespace != ns ||
		st.UserDaemon.ManagerNamespace != managers.ManagerNamespace {
		return nil, false
	}
	return &Conn{r: e.R, ctx: e.Ctx, namespace: ns}, true
}

func destroyConnection(e Env, c *Conn) error {
	if c == nil {
		return nil
	}
	// quit -s stops ALL local daemons and ignores --use, so it is only right
	// for the default connection; a named connection quits just its own
	// session.
	args := []string{"quit", "-s"}
	if c.name != "" {
		args = []string{"quit", "--use", c.name}
	}
	_, stderr, err := e.R.CLI().Run(e.Ctx, args...)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, stderr)
	}
	return nil
}

// Name returns the connection's --name, or "" for the default, unnamed
// connection.
func (c *Conn) Name() string { return c.name }

// useArgs returns ["--use", name] when the connection is named, else nil.
// Every per-connection CLI call appends it, so calls against a named
// connection are routed to it even when other connections are live.
func (c *Conn) useArgs() []string {
	if c.name == "" {
		return nil
	}
	return []string{"--use", c.name}
}

// Status returns the current `telepresence status` snapshot.
func (c *Conn) Status(t testing.TB) *cli.Status {
	t.Helper()
	var st cli.Status
	args := append([]string{"status", "--format", "json"}, c.useArgs()...)
	if err := c.r.CLI().JSON(c.ctx, &st, args...); err != nil {
		t.Fatalf("status: %v", err)
	}
	return &st
}

// List returns the current `telepresence list` entries.
func (c *Conn) List(t testing.TB) []cli.ListEntry {
	t.Helper()
	var entries []cli.ListEntry
	args := append([]string{"list", "--format", "json"}, c.useArgs()...)
	if err := c.r.CLI().JSON(c.ctx, &entries, args...); err != nil {
		t.Fatalf("list: %v", err)
	}
	return entries
}

// Intercept attaches an intercept to wl.
func (c *Conn) Intercept(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "intercept", wl, opts)
}

// Ingest attaches an ingest to wl.
func (c *Conn) Ingest(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "ingest", wl, opts)
}

// Replace attaches a replace to wl: the traffic-agent replaces the
// application container instead of running alongside it.
func (c *Conn) Replace(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "replace", wl, opts)
}

// Wiretap attaches a wiretap to wl: the local handler receives a copy of
// traffic while the cluster's own handler keeps serving it.
func (c *Conn) Wiretap(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "wiretap", wl, opts)
}

func (c *Conn) attach(t testing.TB, verb string, wl *Workload, opts []cli.InterceptOpt) *Attach {
	t.Helper()
	args := []string{verb, wl.Name, "--namespace", wl.Namespace, "--format", "json"}
	switch verb {
	case "intercept", "replace", "wiretap":
		args = append(args, "--detailed-output")
	}
	for _, o := range opts {
		args = append(args, o()...)
	}
	args = append(args, c.useArgs()...)
	stdout, stderr, err := c.r.CLI().Run(c.ctx, args...)
	if err != nil {
		// A client-side attach timeout can leave the intercept behind on the
		// manager, where it would block every later attach to the workload.
		dArgs := append([]string{"detach", wl.Name, "-n", wl.Namespace}, c.useArgs()...)
		_, _, _ = c.r.CLI().Run(c.ctx, dArgs...)
		t.Fatalf("%s %s: %v\nstdout:\n%s\nstderr:\n%s", verb, wl.Name, err, stdout, stderr)
	}
	a := &Attach{conn: c, namespace: wl.Namespace, name: wl.Name}
	switch verb {
	case "intercept":
		var info cli.InterceptInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Intercept = &info
		}
	case "replace":
		var info cli.InterceptInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Replace = &info
		}
	case "wiretap":
		var info cli.InterceptInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Wiretap = &info
		}
	case "ingest":
		var info cli.IngestInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Ingest = &info
		}
	}
	return a
}

// Disconnect quits this connection: all local daemons for the default
// connection, just the named session otherwise (quit -s stops every daemon
// and ignores --use).
func (c *Conn) Disconnect(t testing.TB) {
	t.Helper()
	args := []string{"quit", "-s"}
	if c.name != "" {
		args = []string{"quit", "--use", c.name}
	}
	if stdout, stderr, err := c.r.CLI().Run(c.ctx, args...); err != nil {
		t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
}

// Detach removes the intercept, ingest, replace, or wiretap.
func (a *Attach) Detach(t testing.TB) {
	t.Helper()
	use := a.conn.useArgs()
	args := make([]string, 0, 4+len(use))
	args = append(args, "detach", a.name, "-n", a.namespace)
	args = append(args, use...)
	if stdout, stderr, err := a.conn.r.CLI().Run(a.conn.ctx, args...); err != nil {
		t.Fatalf("detach %s: %v\nstdout:\n%s\nstderr:\n%s", a.name, err, stdout, stderr)
	}
}

// routeCheckTimeout bounds RoutedToLocal/RoutedToCluster.
const routeCheckTimeout = 30 * time.Second

// RoutedToLocal asserts that url is served by ls: the response carries ls's
// marker.
func RoutedToLocal(t testing.TB, url string, ls *LocalService, opts ...check.ReqOpt) {
	t.Helper()
	check.EventuallyHTTP(t, url, check.BodyContains(ls.Marker()), routeCheckTimeout, opts...)
}

// RoutedToCluster asserts that url is NOT served by a LocalService: the
// response is a 200 whose body carries no local-service marker.
func RoutedToCluster(t testing.TB, url string, opts ...check.ReqOpt) {
	t.Helper()
	check.EventuallyHTTP(t, url, notLocalMarker, routeCheckTimeout, opts...)
}

func notLocalMarker(status int, body string) bool {
	return status == http.StatusOK && !strings.Contains(body, localMarkerPrefix)
}
