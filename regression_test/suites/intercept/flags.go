package intercept

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// InterceptFlags proves intercept flag handling: the --port forms, the
// --env-file/--env-json round trip, and the --detailed-output --format json
// shape.
type InterceptFlags struct {
	rt.Suite
}

func init() {
	rt.Register(&InterceptFlags{},
		rt.InArea("intercept"),
		rt.NeedsManager(managers.Default),
	)
}

// portForm builds the --port InterceptOpt for one of the forms intercept
// accepts: local:<svcPort-number>, local:<portName>, and a bare local port
// (which defaults to the workload's sole container port).
type portForm struct {
	name string
	opt  func(local int, wl *rt.Workload) cli.InterceptOpt
}

//nolint:gochecknoglobals // constant test table
var portForms = []portForm{
	{"number", func(local int, wl *rt.Workload) cli.InterceptOpt {
		return cli.Port(local, strconv.Itoa(wl.Port))
	}},
	{"name", func(local int, _ *rt.Workload) cli.InterceptOpt {
		return cli.Port(local, "http")
	}},
	{"bare", func(local int, _ *rt.Workload) cli.InterceptOpt {
		return barePort(local)
	}},
}

// barePort returns an InterceptOpt setting --port <local> without a remote
// identifier. cli.Port always requires local:remote; the CLI itself accepts
// a bare local port and resolves the remote to the workload's sole
// container port, so this is a local, suite-only option builder.
func barePort(local int) cli.InterceptOpt {
	return func() []string { return []string{"--port", strconv.Itoa(local)} }
}

// Test_PortForms proves each --port form routes the intercept to the local
// service: local:<svcPort-number>, local:<portName>, and a bare local port.
func (s *InterceptFlags) Test_PortForms() {
	for _, f := range portForms {
		s.Run(f.name, func() {
			t := s.T()
			conn := s.Connect()
			wl := s.Workload(workloads.Echo("port-form-" + f.name))
			ls := s.LocalEcho()

			a := conn.Intercept(t, wl, f.opt(ls.Port(), wl), cli.MountFalse())
			defer a.Detach(t)
			rt.RoutedToLocal(t, wl.ServiceURL(), ls)
		})
	}
}

// envJSON sets --env-json path. cli/opts.go has EnvFile but not this form.
func envJSON(path string) cli.InterceptOpt {
	return func() []string { return []string{"--env-json", path} }
}

// guaranteedEnvKeys are the keys intercept/state.go's create() always adds
// to the intercept's environment before writing --env-file/--env-json,
// regardless of what the workload's own container declares. The echo
// workload template sets no distinctive env var of its own (see
// workloads/templates/*.yaml), so the round trip asserts on these instead.
//
//nolint:gochecknoglobals // constant
var guaranteedEnvKeys = []string{
	"TELEPRESENCE_ROOT",
	"TELEPRESENCE_INTERCEPT_ID",
	"TELEPRESENCE_API_HOST",
}

// Test_EnvRoundTrip proves --env-file and --env-json both round-trip the
// intercept's environment.
func (s *InterceptFlags) Test_EnvRoundTrip() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("env-round-trip"))
	ls := s.LocalEcho()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.sh")
	envJSONFile := filepath.Join(dir, "env.json")

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(),
		cli.EnvFile(envFile), envJSON(envJSONFile))
	defer a.Detach(t)

	assertEnvFileHasKeys(t, envFile, guaranteedEnvKeys)
	assertEnvJSONHasKeys(t, envJSONFile, guaranteedEnvKeys)
}

// assertEnvFileHasKeys checks that every key appears as "key=" in the
// --env-file output (default dotenv syntax, one KEY=value line per entry).
func assertEnvFileHasKeys(t testing.TB, path string, keys []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading env file %s: %v", path, err)
	}
	text := string(data)
	for _, k := range keys {
		if !strings.Contains(text, k+"=") {
			t.Fatalf("env file %s: missing %s\ncontent:\n%s", path, k, text)
		}
	}
}

// assertEnvJSONHasKeys checks that every key is present in the --env-json
// output.
func assertEnvJSONHasKeys(t testing.TB, path string, keys []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading env json %s: %v", path, err)
	}
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("env json %s: unmarshal: %v", path, err)
	}
	for _, k := range keys {
		if _, ok := env[k]; !ok {
			t.Fatalf("env json %s: missing %s", path, k)
		}
	}
}

// Test_DetailedJSON proves --detailed-output --format json parses into the
// documented shape, with the intercept's name and port populated.
// cli.InterceptInfo mirrors port_id directly (regression_test/framework/cli/
// types.go), so the response Conn.Intercept already parses is enough; no
// raw-stdout parsing needed.
func (s *InterceptFlags) Test_DetailedJSON() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("detailed-json"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	info := a.Intercept
	s.Require().NotNil(info, "intercept response should include InterceptInfo")
	s.Equal(wl.Name, info.Name)
	s.NotEmpty(info.PortID, "expected a populated port_id in the detailed JSON output")
}

// Pairwise matrix axis names.
const (
	axisPort   = "port"
	axisVerb   = "verb"
	axisMount  = "mount"
	axisEnvOut = "env-output"
)

// pairwiseAxes are the flag dimensions Test_PairwiseMatrix covers: how
// --port identifies the remote port, the attach verb, --mount (fixed
// false -- mount-mode combinations are FUSE-dependent and covered in the
// mounts area, not here), and which --env-* form (if any) captures the
// attach's environment.
func pairwiseAxes() []rt.Axis {
	return []rt.Axis{
		{Name: axisPort, Values: []string{"bare", "local:number", "local:name"}},
		{Name: axisVerb, Values: []string{"intercept", "replace"}},
		{Name: axisMount, Values: []string{"false"}},
		{Name: axisEnvOut, Values: []string{"none", "env-file", "env-json"}},
	}
}

// pairwisePortOpt builds the --port InterceptOpt for one pairwiseAxes port
// form, mirroring portForms above (port-form is a fixed-cardinality label
// here rather than a table lookup, since the label itself -- "local:number"
// -- doubles as the pairwise subtest name).
func pairwisePortOpt(form string, local int, wl *rt.Workload) cli.InterceptOpt {
	switch form {
	case "bare":
		return barePort(local)
	case "local:number":
		return cli.Port(local, strconv.Itoa(wl.Port))
	case "local:name":
		return cli.Port(local, "http")
	default:
		panic("pairwise matrix: unknown port form " + form)
	}
}

// Test_PairwiseMatrix runs an all-pairs matrix over pairwiseAxes. One
// workload per verb is reused across every combination that uses it (a
// fresh workload per combination would multiply pod churn for no gain: only
// the verb changes which instrumentation the manager attaches, a sidecar vs
// a full container replacement); combinations run sequentially against it,
// each attaching, asserting, and detaching before the next one attaches.
// Every combination asserts the attach routes to the local service; a
// combination with an --env-* form also asserts the captured file carries
// TELEPRESENCE_ROOT, the marker every attach's environment guarantees (see
// guaranteedEnvKeys above).
func (s *InterceptFlags) Test_PairwiseMatrix() {
	conn := s.Connect()
	ls := s.LocalEcho()
	wlByVerb := map[string]*rt.Workload{
		"intercept": s.Workload(workloads.Echo("pairwise-intercept")),
		"replace":   s.Workload(workloads.Echo("pairwise-replace")),
	}

	axes := pairwiseAxes()
	// replace does not infer a remote mapping from a bare local port the way
	// intercept does (no traffic is forwarded), so that pair is excluded.
	combos := rt.Pairwise(axes, func(c map[string]string) bool {
		return c[axisVerb] == "replace" && c[axisPort] == "bare"
	})
	full := 1
	for _, a := range axes {
		full *= len(a.Values)
	}
	s.T().Logf("pairwise: %d combinations (full product would be %d)", len(combos), full)

	for _, c := range combos {
		s.Run(rt.ComboName(c), func() { s.runPairwiseCombo(conn, ls, wlByVerb, c) })
	}
}

// runPairwiseCombo attaches, asserts, and detaches one Test_PairwiseMatrix
// combination.
func (s *InterceptFlags) runPairwiseCombo(conn *rt.Conn, ls *rt.LocalService, wlByVerb map[string]*rt.Workload,
	c map[string]string,
) {
	t := s.T()
	wl := wlByVerb[c[axisVerb]]

	opts := []cli.InterceptOpt{pairwisePortOpt(c[axisPort], ls.Port(), wl), cli.MountFalse()}
	var envPath string
	switch c[axisEnvOut] {
	case "env-file":
		envPath = filepath.Join(t.TempDir(), "env.sh")
		opts = append(opts, cli.EnvFile(envPath))
	case "env-json":
		envPath = filepath.Join(t.TempDir(), "env.json")
		opts = append(opts, envJSON(envPath))
	}

	var a *rt.Attach
	switch c[axisVerb] {
	case "intercept":
		a = conn.Intercept(t, wl, opts...)
	case "replace":
		// Replace swaps the app container in and out by restarting the pod;
		// back-to-back combinations on one workload must wait for the
		// rollout on both edges or the routing probe races the respawn.
		waitPairwiseRollout(s, wl)
		a = conn.Replace(t, wl, opts...)
		waitPairwiseRollout(s, wl)
		defer waitPairwiseRollout(s, wl)
	default:
		t.Fatalf("pairwise matrix: unknown verb %q", c[axisVerb])
		return
	}
	defer a.Detach(t)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	switch c[axisEnvOut] {
	case "env-file":
		assertEnvFileHasKeys(t, envPath, []string{"TELEPRESENCE_ROOT"})
	case "env-json":
		assertEnvJSONHasKeys(t, envPath, []string{"TELEPRESENCE_ROOT"})
	}
}

// waitPairwiseRollout waits for wl's rollout to settle around a replace
// attach/detach (mirrors suites/attach's waitRollout).
func waitPairwiseRollout(s *InterceptFlags, wl *rt.Workload) {
	kindPath := strings.ToLower(wl.Kind) + "/" + wl.Name
	if _, err := s.R().Kubectl(s.Ctx(), wl.Namespace, "rollout", "status", kindPath, "--timeout=120s"); err != nil {
		s.T().Fatalf("rollout %s: %v", kindPath, err)
	}
}
