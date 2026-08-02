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
	args             []string
	env              []string
	name             string
	docker           bool
	configDir        string
	managerNamespace string
	hashKey          []string
	configErr        error
}

// isDefault reports whether no ConnOpt changed anything: the plain
// `Connect()` case, whose fixture hash and adoption behavior must match the
// pre-M3 framework exactly. len(cs.args) == 0 both covers ConnNamed/
// ConnDocker (already caught by name/docker below) and ConnExtraArgs, which
// touches only args/hashKey: without this check, a connection carrying
// verbatim extra arguments would be misidentified as the plain default and
// become eligible for AdoptFn, silently reusing an already-running plain
// connection instead of one actually started with those arguments.
func (cs *connSpec) isDefault() bool {
	return cs.name == "" && !cs.docker && cs.configDir == "" && len(cs.env) == 0 &&
		cs.managerNamespace == "" && len(cs.args) == 0
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

// ConnExtraArgs appends args verbatim to the connect invocation, folded into
// the fixture hash so a connection using them is never adopted from, or
// memo-shared with, a plain or differently configured connect. Use it for
// connect flags the framework has no dedicated ConnOpt for (e.g.
// --proxy-via, --allow-conflicting-subnets, --mapped-namespaces).
func ConnExtraArgs(args ...string) ConnOpt {
	return func(cs *connSpec) {
		cs.args = append(cs.args, args...)
		cs.hashKey = append(cs.hashKey, "extra-args="+strings.Join(args, "\x1f"))
	}
}

// ConnManagerNamespace overrides --manager-namespace on the connect
// invocation, so the connection targets a manager release living in ns
// instead of the shared one in managers.ManagerNamespace: a SecondaryManager
// (fixture_manager2.go), whose release lives in the namespace it manages
// rather than the shared manager's namespace. Every subsequent CLI call
// against the resulting Conn (list, intercept, ...) needs no equivalent
// override: --manager-namespace only matters at connect time, and the
// daemon keeps talking to whichever manager it connected to.
func ConnManagerNamespace(ns string) ConnOpt {
	return func(cs *connSpec) {
		cs.managerNamespace = ns
		cs.hashKey = append(cs.hashKey, "manager-namespace="+ns)
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
// ensureHostDaemon quits any conflicting daemon before a connection is
// provisioned. Provisioning means "make it fresh": a running daemon may
// hold a different configuration (config dir, KUBECONFIG, namespace) or a
// degraded session — `connect` against it would silently reuse both. The
// healthy-daemon fast path is adoption, which never reaches this. A named
// connection (host or docker) only quits a same-named stale session, so
// establishing it never disturbs another connection (host or docker) that
// happens to already be live; the plain, unnamed host default has no name
// to scope by, so it quits every local daemon instead.
//
//nolint:gochecknoglobals // single, serial test run; see M1 contract
func ensureHostDaemon(e Env, cs *connSpec) {
	if cs.name != "" {
		// A daemon left over from an earlier run would be silently reused by
		// name; quit its session so the connect starts fresh. No -s: that
		// flag stops ALL daemons, host and docker alike, and ignores --use,
		// which would also stop any other named connection already live.
		if _, stderr, err := e.R.CLI().Run(e.Ctx, "quit", "--use", cs.name); err != nil {
			e.R.Infof("[rtest] quit before connect %s: %v: %s", cs.name, err, stderr)
		}
		return
	}
	if cs.docker {
		// An anonymous docker connection has no stale session to target by
		// name, so there is nothing to quit up front.
		return
	}
	if _, stderr, err := e.R.CLI().Run(e.Ctx, "quit", "-s"); err != nil {
		e.R.Infof("[rtest] quit before host connect: %v: %s", err, stderr)
	}
	// That -s took every other live connection with it, host and docker
	// alike, so any memoized elsewhere now names a session that is gone.
	// Forgetting them costs a reconnect on next use; keeping them hands out
	// a dead handle. The connection being provisioned here is not memoized
	// yet -- the engine stores it only once ProvisionFn returns -- so it is
	// not among the entries dropped.
	e.R.ForgetConnections()
}

func connectArgs(ns string, cs *connSpec) []string {
	mgrNS := managers.ManagerNamespace
	if cs.managerNamespace != "" {
		mgrNS = cs.managerNamespace
	}
	args := make([]string, 0, 7+len(cs.args))
	args = append(args,
		"connect",
		"--namespace", ns,
		"--manager-namespace", mgrNS,
		"--as", connectAs,
	)
	return append(args, cs.args...)
}

func connectionHash(args, extra []string) string {
	parts := append(append([]string{}, args...), extra...)
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// ForgetConnections drops every memoized connection fixture, so the next Get
// or Mutate connects afresh. A test that stops the daemons out of band --
// `quit -s` rather than Conn.Disconnect -- must call it: the memo entry
// otherwise survives the daemon and hands the next caller a handle to a
// session that no longer exists, which fails only when some earlier suite
// happened to populate the memo first. Mirrors the invalidation a manager
// roll performs (fixture_manager.go's rollManager).
func (r *Runtime) ForgetConnections() {
	r.engine.invalidateSiblings("connection/", "")
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
			if !cs.isDefault() || e.R.managerRolled {
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
	if err != nil && transientRootDaemonFailure(stderr) {
		// Rapid quit+connect cycles can race the previous root daemon's VIF
		// teardown ("failed to retrieve TAP link: Link not found") or catch
		// the user daemon before its root-daemon client is back; one retry
		// after the device settles is enough.
		e.R.Infof("[rtest] connect %s: transient root-daemon failure, retrying: %s", ns, strings.TrimSpace(stderr))
		time.Sleep(3 * time.Second)
		stdout, stderr, err = e.R.CLIWithEnv(overrides).Run(e.Ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("connect: %w: %s", err, stderr)
	}
	if s := strings.TrimSpace(stdout); s != "" {
		e.R.Infof("[rtest] connect %s: %s", ns, s)
	}
	return &Conn{r: e.R, ctx: e.Ctx, namespace: ns, name: cs.name, docker: cs.docker}, nil
}

// transientRootDaemonFailure reports whether stderr names a root-daemon
// state that clears on its own.
func transientRootDaemonFailure(stderr string) bool {
	return strings.Contains(stderr, "failed to connect to root daemon") ||
		strings.Contains(stderr, "root daemon is reconnecting")
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

// Reconnect issues a raw, unmemoized `connect` to ns and returns the
// resulting *Conn. Unlike Mutate(ConnectionFixture(ns)), it always runs the
// connect command: a fixture only reprovisions after the test that Mutated
// it ends, so a second Mutate on the same (ns, no-opts) hash within one
// test would just return the already-memoized Conn (see
// docs/plans/regression-test-framework/plan.md's "Mutate is single-shot per
// test" note). Callers typically call this right after
// Mutate(ConnectionFixture(ns)).Disconnect(t) to free whatever was
// previously connected.
func Reconnect(t testing.TB, ctx context.Context, ns string, opts ...ConnOpt) *Conn {
	t.Helper()
	r := R()
	args := connectArgs(ns, newConnSpec(opts))
	if _, stderr, err := r.CLI().Run(ctx, args...); err != nil {
		t.Fatalf("connect: %v: %s", err, stderr)
	}
	return &Conn{r: r, ctx: ctx, namespace: ns}
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
	return c.list(t, nil)
}

// ListNamespace is List scoped to ns (`list -n <ns>`), for a workload
// outside the connection's default namespace: e.g. one of several
// namespaces a StaticNamespaces-scoped manager manages.
func (c *Conn) ListNamespace(t testing.TB, ns string) []cli.ListEntry {
	t.Helper()
	return c.list(t, []string{"-n", ns})
}

func (c *Conn) list(t testing.TB, extra []string) []cli.ListEntry {
	t.Helper()
	var entries []cli.ListEntry
	args := make([]string, 0, 2+len(extra)+len(c.useArgs()))
	args = append(args, "list", "--format", "json")
	args = append(args, extra...)
	args = append(args, c.useArgs()...)
	if err := c.r.CLI().JSON(c.ctx, &entries, args...); err != nil {
		t.Fatalf("list: %v", err)
	}
	return entries
}

// Intercept attaches an intercept to wl.
func (c *Conn) Intercept(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "intercept", wl.Name, wl.Namespace, opts)
}

// InterceptNamed attaches an intercept named name to wl, instead of the
// workload's own name Conn.Intercept always reuses as the intercept's
// positional name: for two intercepts sharing one workload, each needs a
// name distinct from the other and from the workload, so --workload takes
// over identifying the target.
func (c *Conn) InterceptNamed(t testing.TB, name string, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	named := make([]cli.InterceptOpt, 0, 1+len(opts))
	named = append(named, cli.WorkloadFlag(wl.Name))
	named = append(named, opts...)
	return c.attach(t, "intercept", name, wl.Namespace, named)
}

// Ingest attaches an ingest to wl.
func (c *Conn) Ingest(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "ingest", wl.Name, wl.Namespace, opts)
}

// Replace attaches a replace to wl: the traffic-agent replaces the
// application container instead of running alongside it.
func (c *Conn) Replace(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "replace", wl.Name, wl.Namespace, opts)
}

// Wiretap attaches a wiretap to wl: the local handler receives a copy of
// traffic while the cluster's own handler keeps serving it.
func (c *Conn) Wiretap(t testing.TB, wl *Workload, opts ...cli.InterceptOpt) *Attach {
	t.Helper()
	return c.attach(t, "wiretap", wl.Name, wl.Namespace, opts)
}

func (c *Conn) attach(t testing.TB, verb, name, namespace string, opts []cli.InterceptOpt) *Attach {
	t.Helper()
	args := []string{verb, name, "--namespace", namespace, "--format", "json"}
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
		dArgs := append([]string{"detach", name, "-n", namespace}, c.useArgs()...)
		_, _, _ = c.r.CLI().Run(c.ctx, dArgs...)
		t.Fatalf("%s %s: %v\nstdout:\n%s\nstderr:\n%s", verb, name, err, stdout, stderr)
	}
	a := &Attach{conn: c, namespace: namespace, name: name}
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
	if c.name == "" {
		// -s stopped every daemon, not just this connection's, so every
		// memoized connection is now stale -- including ones this caller
		// never touched and so never invalidated through Mutate.
		c.r.ForgetConnections()
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

// MountRoot returns the local mount root path recorded in a's captured
// info (whichever of Intercept/Replace/Wiretap/Ingest is set): the
// TELEPRESENCE_ROOT entry the CLI adds to the attach's environment before
// printing it (pkg/client/cli/intercept/state.go's create():
// s.env["TELEPRESENCE_ROOT"] = intercept.ClientMountPoint;
// pkg/client/cli/ingest/state.go's run(): env["TELEPRESENCE_ROOT"] =
// s.info.ClientMountPoint). It is the same directory `--mount` names or
// telepresence auto-picks, and matches os.Getenv("TELEPRESENCE_ROOT") in a
// `--run`/`--run-shell` child.
//
// ok is false when a carries no captured info, or its Environment is nil.
// For intercept/replace/wiretap specifically, a nil Environment is possible
// even though TELEPRESENCE_ROOT was set: state.go aliases s.env onto the
// intercepted container's own (manager-reported) environment map before
// mutating it (s.env = intercept.Environment; s.env["TELEPRESENCE_ROOT"] =
// ...), so the addition only lands in the JSON output's "environment" field
// (pkg/client/cli/intercept/info.go's Info.Environment) when that map was
// already non-nil, i.e. the intercepted container itself reported at least
// one env var. Ingest has no such gap: ingest/state.go reassigns the map
// back onto Info.Environment even when it started nil, so TELEPRESENCE_ROOT
// is always present there.
func MountRoot(a *Attach) (string, bool) {
	var env map[string]string
	switch {
	case a.Intercept != nil:
		env = a.Intercept.Environment
	case a.Replace != nil:
		env = a.Replace.Environment
	case a.Wiretap != nil:
		env = a.Wiretap.Environment
	case a.Ingest != nil:
		env = a.Ingest.Environment
	}
	if env == nil {
		return "", false
	}
	root, ok := env["TELEPRESENCE_ROOT"]
	return root, ok
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

// RoutedToClusterAndTapped asserts that url keeps being served by the cluster
// while a wiretap copies it to ls, by probing until one response satisfies
// RoutedToCluster's condition and ls has observed at least one copy.
//
// Both halves have to be polled together. Attaching evicts the workload's pod
// so the webhook can inject the traffic-agent, and a Deployment's replacement
// pod becomes ready while the original -- which has no agent, and so copies
// nothing -- is still serving, so a probe answered during that window produces
// no copy at all. The copy is also async and lossy: the agent sends it on a
// background goroutine independent of the real request and response (see
// cmd/traffic/cmd/agent/fwd/http.go's handleHTTPRequest), so it can lag the
// response that triggered it.
func RoutedToClusterAndTapped(t testing.TB, url string, ls *LocalService, timeout time.Duration, opts ...check.ReqOpt) {
	t.Helper()
	check.EventuallyHTTP(t, url, func(status int, body string) bool {
		return notLocalMarker(status, body) && len(ls.Requests()) > 0
	}, timeout, opts...)
}

func notLocalMarker(status int, body string) bool {
	return status == http.StatusOK && !strings.Contains(body, localMarkerPrefix)
}
