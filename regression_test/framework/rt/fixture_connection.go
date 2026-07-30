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

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
)

// Conn is a live `telepresence connect` session.
type Conn struct {
	r         *Runtime
	ctx       context.Context
	namespace string
}

// Attach is a live intercept or ingest.
type Attach struct {
	conn      *Conn
	namespace string
	name      string
	Intercept *cli.InterceptInfo
	Ingest    *cli.IngestInfo
}

// connectAs is the --as identity connections use: the test ServiceAccount
// managerRbac/clientRbac grant access to.
const connectAs = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount

func connectArgs(ns string, opts []cli.ConnectOpt) []string {
	args := []string{
		"connect",
		"--namespace", ns,
		"--manager-namespace", managers.ManagerNamespace,
		"--as", connectAs,
	}
	for _, o := range opts {
		args = append(args, o()...)
	}
	return args
}

// ConnectionFixture is a `telepresence connect` session to ns, keyed by
// (namespace, opts). Owns quit-on-teardown.
func ConnectionFixture(ns string, opts ...cli.ConnectOpt) *Fixture[*Conn] {
	args := connectArgs(ns, opts)
	h := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	hash := hex.EncodeToString(h[:])
	return &Fixture[*Conn]{
		Name: "connection/" + ns,
		Hash: hash,
		ProvisionFn: func(e Env) (*Conn, error) {
			return provisionConnection(e, ns, args)
		},
		AdoptFn: func(e Env) (*Conn, bool) {
			return adoptConnection(e, ns)
		},
		DestroyFn: destroyConnection,
	}
}

func provisionConnection(e Env, ns string, args []string) (*Conn, error) {
	stdout, stderr, err := e.R.CLI().Run(e.Ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("connect: %w: %s", err, stderr)
	}
	if s := strings.TrimSpace(stdout); s != "" {
		e.R.Infof("[rtest] connect %s: %s", ns, s)
	}
	return &Conn{r: e.R, ctx: e.Ctx, namespace: ns}, nil
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
	_, stderr, err := e.R.CLI().Run(e.Ctx, "quit", "-s")
	if err != nil {
		return fmt.Errorf("quit -s: %w: %s", err, stderr)
	}
	return nil
}

// Status returns the current `telepresence status` snapshot.
func (c *Conn) Status(t testing.TB) *cli.Status {
	t.Helper()
	var st cli.Status
	if err := c.r.CLI().JSON(c.ctx, &st, "status", "--format", "json"); err != nil {
		t.Fatalf("status: %v", err)
	}
	return &st
}

// List returns the current `telepresence list` entries.
func (c *Conn) List(t testing.TB) []cli.ListEntry {
	t.Helper()
	var entries []cli.ListEntry
	if err := c.r.CLI().JSON(c.ctx, &entries, "list", "--format", "json"); err != nil {
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

func (c *Conn) attach(t testing.TB, verb string, wl *Workload, opts []cli.InterceptOpt) *Attach {
	t.Helper()
	args := []string{verb, wl.Name, "--namespace", wl.Namespace, "--format", "json"}
	if verb == "intercept" {
		args = append(args, "--detailed-output")
	}
	for _, o := range opts {
		args = append(args, o()...)
	}
	stdout, stderr, err := c.r.CLI().Run(c.ctx, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", verb, wl.Name, err, stderr)
	}
	a := &Attach{conn: c, namespace: wl.Namespace, name: wl.Name}
	switch verb {
	case "intercept":
		var info cli.InterceptInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Intercept = &info
		}
	case "ingest":
		var info cli.IngestInfo
		if err := json.Unmarshal([]byte(stdout), &info); err == nil {
			a.Ingest = &info
		}
	}
	return a
}

// Disconnect quits the connection's daemons.
func (c *Conn) Disconnect(t testing.TB) {
	t.Helper()
	if _, stderr, err := c.r.CLI().Run(c.ctx, "quit", "-s"); err != nil {
		t.Fatalf("quit -s: %v\n%s", err, stderr)
	}
}

// Detach removes the intercept or ingest.
func (a *Attach) Detach(t testing.TB) {
	t.Helper()
	args := []string{"detach", a.name, "-n", a.namespace}
	if _, stderr, err := a.conn.r.CLI().Run(a.conn.ctx, args...); err != nil {
		t.Fatalf("detach %s: %v\n%s", a.name, err, stderr)
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
