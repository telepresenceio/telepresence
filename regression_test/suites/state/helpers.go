package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/state"
)

// This area's manifests declare their own `connection:` block, so every
// test in this package connects and disconnects for itself instead of
// through the shared s.Connect() fixture (regression_test/README.md's
// Connections rule: "Anything that changes ... the default connection ...
// must go through Mutate, or later tests inherit your leftovers" -- a
// manifest-driven connection isn't expressible as a Mutate at all, since
// apply/delete own its whole lifecycle). quitAll is every test's final
// safety net, called directly or via defer, so the area never leaves a
// daemon running for whichever test or area runs next.

const (
	// stateConnName names the connection every manifest in this package
	// declares. The framework's host (non-docker) daemon is a process
	// singleton with a single info file regardless of --name
	// (pkg/client/cli/daemon/identifier.go's Identifier.InfoFileName), so
	// reusing one name across every test in this package is equivalent to
	// each test picking its own: only one such session can ever be live at
	// a time, and quitAll guarantees none survives past a test's end.
	stateConnName = "rtest-state"

	// stateInterceptWL/stateIngestWL name the two echo workloads every
	// manifest in this package attaches to: an intercept target and a
	// sibling ingest target, so one manifest covers both attachment kinds.
	stateInterceptWL = "state-intercept"
	stateIngestWL    = "state-ingest"

	// stateLocalPort is the intercept attachment's declared local port.
	// Unlike rt.Suite.LocalEcho()'s :0 auto-assignment, it names no real
	// listener: pkg/client/cli/intercept/state.go's Run never binds it (the
	// port is only metadata the manager routes intercepted traffic to), so
	// it needs no free-port discovery either -- except once the Handler
	// suite's `command:` starts a process, and even that process never
	// binds the port itself.
	stateLocalPort = 19080
)

// manifestResult/attachmentResult mirror pkg/client/cli/manifest/report.go's
// summary/attachmentResult: the object `apply`/`delete --output json` print
// on success. manifest.Apply/Delete's printSummary calls output.Object(...,
// override=true); per pkg/client/cli/output/output.go's Execute(), an
// error-free --output json invocation then prints that object verbatim, with
// no {cmd,stdout,stderr,err} envelope -- only a failing invocation falls
// back to the envelope, which is why applyJSON/deleteJSON below only cover
// the success path.
type manifestResult struct {
	Connection  string             `json:"connection,omitempty"`
	Attachments []attachmentResult `json:"attachments,omitempty"`
}

type attachmentResult struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Action  string `json:"action"`
	Detail  string `json:"detail,omitempty"`
	Handler string `json:"handler,omitempty"`
}

// attachment returns the result for the named attachment, failing t if none
// is present.
func (r manifestResult) attachment(t testing.TB, name string) attachmentResult {
	t.Helper()
	for _, a := range r.Attachments {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("attachment %q not present in result: %+v", name, r)
	return attachmentResult{}
}

// stateAttachments returns the intercept (mount disabled, local port
// localPort) and sibling ingest (mount disabled) attachment pair shared by
// every connection-bearing manifest in this package. The intercept's local
// port is the only field a drift variant ever changes.
func stateAttachments(localPort int) []state.Attachment {
	ic := state.Intercept(stateInterceptWL)
	ic.Ports = []string{fmt.Sprintf("%d:http", localPort)}
	ic.Mount = &state.Mount{Enabled: state.Bool(false)}

	ig := state.Ingest(stateIngestWL)
	ig.Mount = &state.Mount{Enabled: state.Bool(false)}

	return []state.Attachment{ic, ig}
}

// connectionManifest returns a WorkstationState declaring the shared
// connection (stateConnName, in ns) plus the intercept+ingest attachment
// pair, with mappedNamespaces drifted from the live session's implicit
// (namespace-scoped) mapping when mappedNS is set -- a namespace-scoped
// session maps exactly its own namespace, so the two-entry list can never
// align with it. Mirrors writeManifestWithConnection.
func connectionManifest(ns string, localPort int, mappedNS bool) *state.State {
	conn := &state.Connection{
		Name:             stateConnName,
		Namespace:        ns,
		ManagerNamespace: managers.ManagerNamespace,
	}
	if mappedNS {
		conn.MappedNamespaces = []string{ns, managers.ManagerNamespace}
	}
	return &state.State{Connection: conn, Attachments: stateAttachments(localPort)}
}

// connectionlessManifest returns a WorkstationState with the same
// attachments as connectionManifest but no connection block: apply/delete
// then depend on an already established session. Mirrors
// writeManifestWithoutConnection.
func connectionlessManifest(localPort int) *state.State {
	return &state.State{Attachments: stateAttachments(localPort)}
}

// writeManifest renders st under ArtifactDir("state") and returns its path.
func writeManifest(t testing.TB, r *rt.Runtime, ctx context.Context, st *state.State) string {
	t.Helper()
	path, err := st.Write(rt.Env{Ctx: ctx, T: t, R: r})
	if err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return path
}

// applyJSON runs `telepresence apply -f path --output json` (adding
// --dry-run when dryRun is set), requires it to succeed, and unmarshals the
// resulting summary. A case expected to fail (drift, missing session) must
// use tp.Run directly and check stderr text instead: --output json's error
// path is the deprecated {cmd,stdout,stderr,err} envelope, not the clean
// summary (see manifestResult's doc comment).
func applyJSON(t testing.TB, tp *cli.TP, ctx context.Context, path string, dryRun bool) manifestResult {
	t.Helper()
	args := []string{"apply", "-f", path, "--output", "json"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	var res manifestResult
	if err := tp.JSON(ctx, &res, args...); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

// deleteJSON runs `telepresence delete -f path --output json`, requires it
// to succeed, and unmarshals the resulting summary. It covers every
// delete-side case this package needs, including the "not connected"/"not
// running; nothing to tear down" variants: neither is an error, per
// pkg/client/cli/manifest/delete.go's Delete.
func deleteJSON(t testing.TB, tp *cli.TP, ctx context.Context, path string) manifestResult {
	t.Helper()
	var res manifestResult
	if err := tp.JSON(ctx, &res, "delete", "-f", path, "--output", "json"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	return res
}

// quitAll stops every local daemon. quit is always safe to call, even when
// nothing is running (smoke.SmokeCLI's Test_StatusNotRunning relies on the
// same guarantee), so a failure here is a genuine error worth failing the
// test over.
//
// The daemons go away behind the fixture engine's back, so any memoized
// connection is now a handle to a dead session: ForgetConnections drops
// them, and the next Get or Mutate connects afresh. Without it, a test in
// this package that quits and a later one that takes the shared connection
// fixture pass or fail depending on whether some earlier area populated
// that memo.
func quitAll(t testing.TB, tp *cli.TP, ctx context.Context) {
	t.Helper()
	if _, stderr, err := tp.Run(ctx, "quit", "-s"); err != nil {
		t.Fatalf("quit -s: %v\n%s", err, stderr)
	}
	rt.R().ForgetConnections()
}

// listHasWorkload reports whether entries contains a workload named name.
func listHasWorkload(entries []cli.ListEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// handlerRecordPath returns the path apply/delete's handler bookkeeping
// (pkg/client/cli/manifest/handler.go's handlerStateFile) writes the named
// attachment's pid/argv record to, under the run's user cache dir
// (rt.Runtime.UserCacheDir). The daemon owning it is the non-containerized
// (host) daemon this package always connects through, whose info file is
// always named daemon.InfoFileName regardless of the manifest connection's
// name (pkg/client/cli/daemon/identifier.go's Identifier.InfoFileName: only
// a containerized identifier's info file is name-derived).
func handlerRecordPath(r *rt.Runtime, attachmentName string) string {
	return filepath.Join(r.UserCacheDir(), "handlers", daemon.InfoFileName, ioutil.SafeName(attachmentName)+".json")
}
