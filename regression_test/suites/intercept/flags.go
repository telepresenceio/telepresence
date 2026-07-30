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

// detailedInfo adds the port_id field that the real `intercept
// --detailed-output --format json` output carries but cli.InterceptInfo
// (the framework's minimal mirror; regression_test/framework/cli/types.go)
// doesn't expose.
type detailedInfo struct {
	cli.InterceptInfo
	PortID string `json:"port_id,omitempty"`
}

// Test_DetailedJSON proves --detailed-output --format json parses into the
// documented shape, with the intercept's name and port populated.
func (s *InterceptFlags) Test_DetailedJSON() {
	t := s.T()
	s.Connect()
	wl := s.Workload(workloads.Echo("detailed-json"))
	ls := s.LocalEcho()
	tp, ctx := s.CLI(), s.Ctx()

	stdout, stderr, err := namedIntercept(t, tp, ctx, wl, wl.Name, rt.ToLocal(ls, "http"), cli.MountFalse())
	if err != nil {
		t.Fatalf("intercept %s: %v\nstdout:\n%s\nstderr:\n%s", wl.Name, err, stdout, stderr)
	}
	defer detachNamed(t, tp, ctx, wl.Name, wl.Namespace)

	var info detailedInfo
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("unmarshal intercept JSON: %v\noutput:\n%s", err, stdout)
	}
	if info.Name != wl.Name {
		t.Fatalf("expected name %q, got %q", wl.Name, info.Name)
	}
	if info.PortID == "" {
		t.Fatalf("expected a populated port_id in the detailed JSON output")
	}
}
