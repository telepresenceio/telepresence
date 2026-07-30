package intercept

import (
	"context"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// namedIntercept runs `telepresence intercept <name> --workload <wl.Name>
// --namespace <wl.Namespace> --format json --detailed-output <opts...>`
// directly against tp, returning the raw stdout/stderr. Conn.Intercept
// always derives the intercept's positional name from wl.Name, so tests
// that need a name distinct from the workload (two intercepts sharing one
// workload) or the raw JSON stdout (cli.InterceptInfo mirrors only a
// subset of the real output) call this instead.
func namedIntercept(t testing.TB, tp *cli.TP, ctx context.Context, wl *rt.Workload, name string,
	opts ...cli.InterceptOpt,
) (stdout, stderr string, err error) {
	t.Helper()
	args := []string{
		"intercept", name, "--workload", wl.Name, "--namespace", wl.Namespace,
		"--format", "json", "--detailed-output",
	}
	for _, o := range opts {
		args = append(args, o()...)
	}
	return tp.Run(ctx, args...)
}

// detachNamed runs `telepresence detach <name> -n <ns>`, for an attachment
// created via namedIntercept.
func detachNamed(t testing.TB, tp *cli.TP, ctx context.Context, name, ns string) {
	t.Helper()
	if stdout, stderr, err := tp.Run(ctx, "detach", name, "-n", ns); err != nil {
		t.Fatalf("detach %s: %v\nstdout:\n%s\nstderr:\n%s", name, err, stdout, stderr)
	}
}
