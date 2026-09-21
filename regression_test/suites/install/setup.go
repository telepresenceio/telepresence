package install

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	rbac "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// Setup drives `telepresence setup` (pkg/client/cli/cmd/setup.go, whose
// semantics live in pkg/client/cli/setup/) against fresh
// PrivateUnmanagedNamespaces, never the shared release: idempotent
// apply/doctor output, the --input/--output values-file round trip,
// --output - streaming, upgrade-time value merging, the non-admin handoff,
// the clientRbac passthrough, and the plain-validation report. Like
// HelmLifecycle, it manages its own installs and never touches the shared
// release.
type Setup struct {
	rt.Suite
}

func init() {
	rt.Register(&Setup{}, rt.InArea("install"))
}

// releaseExists checks for the traffic-manager Helm release secret directly
// in ns, mirroring framework/rt/fixture_manager.go's managerReleaseExists
// (unexported there, so duplicated here rather than imported).
func releaseExists(ctx context.Context, r *rt.Runtime, ns string) (bool, error) {
	out, err := r.Kubectl(ctx, ns, "get", "secret", "-l", "owner=helm", "-o", "name")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "sh.helm.release.v1."+managerReleaseName+"."), nil
}

// uninstallIfPresent uninstalls the traffic-manager release in ns, guarded
// by releaseExists so a test that never applied anything doesn't uninstall
// against nothing. Registered via t.Cleanup by every test below, since any
// of them can end up installing a release (setup --apply, or a raw helm
// install used to seed an upgrade).
func uninstallIfPresent(t testing.TB, ctx context.Context, r *rt.Runtime, ns string) {
	t.Helper()
	ok, err := releaseExists(ctx, r, ns)
	if err != nil || !ok {
		return
	}
	if _, stderr, err := r.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns); err != nil {
		t.Logf("[rtest] setup: helm uninstall in %s: %v: %s", ns, err, stderr)
	}
}

// helmGetValues reads the traffic-manager release's stored values (the
// operator-supplied Config a `helm get values` call returns) via the real
// helm binary: the CLI under test only wires helm install/upgrade/uninstall/
// lint/version (pkg/client/cli/cmd/helm.go), no read verb equivalent to
// `helm get values`. It relies on the run's KUBECONFIG (with its
// current-context already pinned to RTEST_CONTEXT by
// framework/rt/kubeconfig.go's pinContextKubeconfig) rather than an explicit
// --kube-context flag, exactly the mechanism that file documents.
func helmGetValues(ctx context.Context, r *rt.Runtime, ns string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, "helm", "get", "values", managerReleaseName, "-n", ns, "-o", "json")
	cmd.Env = r.CLI().Env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm get values -n %s: %w", ns, err)
	}
	var values map[string]any
	if err := json.Unmarshal(out, &values); err != nil {
		return nil, fmt.Errorf("helm get values -n %s: unmarshal: %w", ns, err)
	}
	return values, nil
}

// applyManifest writes manifest under ArtifactDir("manifests") and applies
// it with `kubectl apply -f`, mirroring framework/rt/runtime.go's
// identically named, unexported helper (not reachable from this package).
func applyManifest(ctx context.Context, r *rt.Runtime, tag, manifest string) error {
	path := filepath.Join(r.ArtifactDir("manifests"), tag+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, err := r.Kubectl(ctx, "", "apply", "-f", path)
	return err
}

// restrictedNodesManifest creates a ServiceAccount plus a ClusterRole/
// ClusterRoleBinding granting it only get/list on nodes (a plain
// authenticated identity can always issue SelfSubjectAccessReviews via the
// built-in system:basic-user binding, so probeRBAC's sweep still runs; see
// pkg/client/cli/setup/probe_rbac.go). %[1]s is the ServiceAccount name,
// %[2]s its namespace, %[3]s the (cluster-unique) ClusterRole/
// ClusterRoleBinding name.
const restrictedNodesManifest = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[1]s
  namespace: %[2]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %[3]s
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %[3]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
roleRef:
  kind: ClusterRole
  name: %[3]s
  apiGroup: rbac.authorization.k8s.io
`

// loadValuesFile reads a setup --output values file (a YAML document with a
// leading "# ..." provenance comment; see pkg/client/cli/setup/render.go's
// ProvenanceHeader/WriteValuesFile) into a map, comments and all -- YAML
// comments are simply not data.
func loadValuesFile(t testing.TB, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values
}

// writeValuesFile marshals values as YAML to path.
func writeValuesFile(t testing.TB, path string, values map[string]any) {
	t.Helper()
	data, err := yaml.Marshal(values)
	if err != nil {
		t.Fatalf("marshaling %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// stripFullLineComments returns path's content with every "#"-prefixed line
// removed, for comparing two setup --output files that should be identical
// except for the provenance header's timestamp line.
func stripFullLineComments(t testing.TB, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// setNestedValue sets values at the given key path, creating intermediate
// maps as needed.
func setNestedValue(values map[string]any, path []string, v any) {
	m := values
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

// valueAtPath reads a nested value at the given key path.
func valueAtPath(values map[string]any, path ...string) (any, bool) {
	var cur any = values
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Test_ApplyIdempotence installs with `setup --apply --non-interactive`,
// repeats the identical apply and expects a no-op, reads the same state back
// as --format json, then scales the StatefulSet to zero and confirms the
// doctor checks (pkg/client/cli/setup/probe_health.go's probeHealth,
// rendered by render.go's printFindings) notice.
func (s *Setup) Test_ApplyIdempotence() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "apply")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	args := []string{"setup", "--manager-namespace", ns, "--apply", "--non-interactive"}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "setup --apply (install): %s", stderr)
	for _, want := range []string{
		"Action: install",
		"This install will create:",
		"removed by 'telepresence helm uninstall'",
		"Applying...",
		"Traffic Manager installed successfully",
		"Verification:",
	} {
		s.Contains(stdout, want)
	}
	ok, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.True(ok, "a release should exist after the install")

	// An identical apply is a no-op: recommend.go's decideAction sees no
	// ChangedKeys against the release it just installed.
	stdout, stderr, err = s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "setup --apply (idempotent): %s", stderr)
	s.Contains(stdout, "Action: none")
	s.Contains(stdout, "up to date")
	s.NotContains(stdout, "Applying...")
	for _, want := range []string{
		"health: traffic-manager yes",
		"health: quic endpoint yes",
		"health: version skew yes",
	} {
		s.Contains(stdout, want)
	}

	dir := r.ArtifactDir("setup")
	f5 := filepath.Join(dir, "idempotence-"+ns+"-f5.yaml")
	f6 := filepath.Join(dir, "idempotence-"+ns+"-f6.yaml")

	// --format json produces the same Summary (render.go's PrintReport)
	// as a clean structured object instead of sectioned text.
	jargs := []string{"setup", "--manager-namespace", ns, "--non-interactive", "--format", "json", "--output", f5}
	stdout, stderr, err = s.CLI().Run(ctx, jargs...)
	s.Require().NoError(err, "setup --format json: %s", stderr)
	var summary map[string]any
	s.Require().NoError(json.Unmarshal([]byte(stdout), &summary), "parsing --format json output")
	s.Equal("none", summary["action"])
	facts, ok2 := summary["facts"].(map[string]any)
	s.Require().True(ok2, "facts should be a JSON object")
	s.Contains(facts, "health")

	// Scale to zero: probeHealth's healthManager (probe_health.go) reports
	// the StatefulSet unready with "scaled to zero replicas" evidence.
	_, err = r.Kubectl(ctx, ns, "scale", trafficManagerStatefulSet, "--replicas=0")
	s.Require().NoError(err)
	stdout, stderr, err = s.CLI().Run(ctx, "setup", "--manager-namespace", ns, "--non-interactive", "--output", f6)
	s.Require().NoError(err, "setup after scale-to-zero: %s", stderr)
	s.Contains(stdout, "health: traffic-manager no")
	s.Contains(stdout, "scaled to zero")
}

// Test_RoundTrip proves that `setup --output` followed by `setup --input
// <that file> --output` reproduces the same values (ReconcileWithInput,
// pkg/client/cli/setup/input.go, passes an unchanged recommendation
// straight through when the input already matches it) and that a custom
// value the engine has no opinion about survives untouched. None of these
// runs pass --apply, so no release may exist afterward.
func (s *Setup) Test_RoundTrip() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "roundtrip")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	// The routing probe (pkg/client/cli/setup/probe_routing.go) reads the
	// live route table: a running connection's TUN device turns the cluster
	// subnets into local-route conflicts, making the recommendation carry
	// client.routing.allowConflictingSubnets -- and the pinned-input leg
	// below would then trip the pin warning legitimately. Quit every daemon
	// first so the probes see a clean workstation, and drop the memoized
	// connections that quit invalidated.
	if _, stderr, err := s.CLI().Run(ctx, "quit", "-s"); err != nil {
		t.Fatalf("quit -s: %v\n%s", err, stderr)
	}
	r.ForgetConnections()

	dir := r.ArtifactDir("setup")
	f1 := filepath.Join(dir, "roundtrip-"+ns+"-f1.yaml")
	f2 := filepath.Join(dir, "roundtrip-"+ns+"-f2.yaml")
	f3 := filepath.Join(dir, "roundtrip-"+ns+"-f3.yaml")
	f4 := filepath.Join(dir, "roundtrip-"+ns+"-f4.yaml")

	_, stderr, err := s.CLI().Run(ctx, "setup", "--manager-namespace", ns, "--non-interactive", "--output", f1)
	s.Require().NoError(err, "setup --output f1: %s", stderr)

	stdout, stderr, err := s.CLI().Run(ctx,
		"setup", "--manager-namespace", ns, "--non-interactive", "--input", f1, "--output", f2)
	s.Require().NoError(err, "setup --input f1 --output f2: %s", stderr)
	s.NotContains(stdout, "warning: the input pins")
	s.NotContains(stderr, "warning: the input pins")

	for _, f := range []string{f1, f2} {
		data, err := os.ReadFile(f)
		s.Require().NoError(err)
		s.Contains(string(data), "# Generated by telepresence setup ")
	}
	s.Equal(stripFullLineComments(t, f1), stripFullLineComments(t, f2), "f1 and f2 should carry identical values")

	// f3 adds a plain passthrough (logLevel) and a pinned custom value the
	// engine never touches (client.routing.allowConflictingSubnets) on top
	// of f1's content; both must survive verbatim into f4.
	values := loadValuesFile(t, f1)
	values["logLevel"] = "debug"
	setNestedValue(values, []string{"client", "routing", "allowConflictingSubnets"}, []any{"10.88.0.0/16"})
	writeValuesFile(t, f3, values)

	stdout, stderr, err = s.CLI().Run(ctx,
		"setup", "--manager-namespace", ns, "--non-interactive", "--input", f3, "--output", f4)
	s.Require().NoError(err, "setup --input f3 --output f4: %s", stderr)
	s.NotContains(stdout, "warning: the input pins")
	s.NotContains(stdout, "Local routes overlap")
	s.NotContains(stderr, "warning: the input pins")
	s.NotContains(stderr, "Local routes overlap")

	f4Values := loadValuesFile(t, f4)
	s.Equal("debug", f4Values["logLevel"])
	subnets, ok := valueAtPath(f4Values, "client", "routing", "allowConflictingSubnets")
	s.Require().True(ok, "client.routing.allowConflictingSubnets should survive into f4")
	s.Equal([]any{"10.88.0.0/16"}, subnets)

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "round trips must not create a release")
}

// Test_StreamOutput checks `setup --output -`: the values stream to stdout
// as plain YAML (render.go's WriteValues) with no report mixed in, since
// emit() skips PrintReport entirely when the values themselves own stdout.
func (s *Setup) Test_StreamOutput() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "stream")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	stdout, stderr, err := s.CLI().Run(ctx, "setup", "--manager-namespace", ns, "--non-interactive", "--output", "-")
	s.Require().NoError(err, "setup --output -: %s", stderr)

	var values map[string]any
	s.Require().NoError(yaml.Unmarshal([]byte(stdout), &values), "stdout should parse as YAML values")
	s.Contains(values, "quicTunnel")
	s.Contains(values, "nodeAgent")
	s.Contains(stdout, "# Generated by telepresence setup ")

	for _, out := range []string{stdout, stderr} {
		s.NotContains(out, "Findings:")
		s.NotContains(out, "Action:")
	}

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "--output - must not create a release")
}

// Test_UpgradeMerge seeds a release the way HelmLifecycle/HelmValues do (a
// raw `telepresence helm install`, here with an extra --set logLevel=debug
// to give the merge something of its own to preserve), then runs `setup
// --apply --non-interactive` and checks that the upgrade merges the
// recommendation into the existing values (recommend.go's decideAction:
// chartutil.CoalesceTables(vals, facts.Release.Values)) rather than
// replacing them. helmInstallArgs (helpers.go) only sets namespaces/image.*,
// never agentInjector/nodeAgent/quicTunnel, so those three keys are always
// the ones setup's recommendation introduces -- diffKeys flags a key the
// moment it's absent from the base release, regardless of its value.
func (s *Setup) Test_UpgradeMerge() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "upgrade")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	installArgs := append(helmInstallArgs(r, ns), "--set", "logLevel=debug")
	_, stderr, err := s.CLI().Run(ctx, installArgs...)
	s.Require().NoError(err, "helm install: %s", stderr)
	requireManagerReady(t, ctx, r, ns)

	stdout, stderr, err := s.CLI().Run(ctx, "setup", "--manager-namespace", ns, "--apply", "--non-interactive")
	s.Require().NoError(err, "setup --apply: %s", stderr)

	s.Contains(stdout, "release: traffic-manager")
	s.Contains(stdout, "Changed from current installation:")
	for _, key := range []string{"agentInjector.enabled", "nodeAgent.enabled", "quicTunnel.enabled"} {
		s.Contains(stdout, "  - "+key)
	}
	s.Contains(stdout, "Action: upgrade")
	s.Contains(stdout, "Traffic Manager upgraded successfully")

	values, err := helmGetValues(ctx, r, ns)
	s.Require().NoError(err)
	s.Equal("debug", values["logLevel"], "the pre-existing logLevel should survive the merge")
	nodeAgentEnabled, ok := valueAtPath(values, "nodeAgent", "enabled")
	s.Require().True(ok, "nodeAgent.enabled should be set")
	s.Equal(true, nodeAgentEnabled)
}

// Test_NonAdminHandoff runs setup impersonating a ServiceAccount that can
// only get/list nodes -- probeRBAC's SelfSubjectAccessReview sweep still
// works for it (every authenticated identity gets the built-in
// system:basic-user binding), but every create check is denied, so
// checkPrivileges (recommend.go) downgrades the denial to a warning plus
// handoff instructions instead of aborting, since --apply was not passed.
func (s *Setup) Test_NonAdminHandoff() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "noadmin")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	sa := "setup-noadmin"
	clusterRoleName := "tel-rtest-setup-noadmin-" + ns
	manifest := fmt.Sprintf(restrictedNodesManifest, sa, ns, clusterRoleName)
	s.Require().NoError(applyManifest(ctx, r, "setup-noadmin-"+ns, manifest))
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, "", "delete", "clusterrolebinding", clusterRoleName, "--ignore-not-found")
		_, _ = r.Kubectl(ctx, "", "delete", "clusterrole", clusterRoleName, "--ignore-not-found")
	})

	dir := r.ArtifactDir("setup")
	input := filepath.Join(dir, "noadmin-"+ns+"-input.yaml")
	values := filepath.Join(dir, "noadmin-"+ns+"-values.yaml")
	writeValuesFile(t, input, map[string]any{
		"agentInjector": map[string]any{"enabled": false},
		"nodeAgent":     map[string]any{"enabled": false},
	})

	identity := "system:serviceaccount:" + ns + ":" + sa
	stdout, stderr, err := s.CLI().Run(ctx,
		"setup", "--manager-namespace", ns, "--as", identity,
		"--non-interactive", "--input", input, "--output", values)
	s.Require().NoError(err, "setup --as %s: %s", identity, stderr)

	for _, want := range []string{
		"insufficient privileges",
		"an admin can complete this install",
		"missing privileges listed above",
		"--input FILE --apply",
		"Action: would-install",
	} {
		s.Contains(stdout, want)
	}
	_, statErr := os.Stat(values)
	s.NoError(statErr, "the values file should have been written")

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "a denied, non-applied install must not create a release")
}

// Test_ClientRbacInputRoundTrip proves that an input's clientRbac block
// round-trips unchanged aside from the engine's own legacyAccess opinion:
// recommend.go has no opinion about clientRbac.create/subjects, so
// ReconcileWithInput's deep clone (input.go) carries them through verbatim,
// while clientRbac.legacyAccess is always merged in from the answer (default
// false, since the input here doesn't pin it).
func (s *Setup) Test_ClientRbacInputRoundTrip() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "clientrbac")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	dir := r.ArtifactDir("setup")
	input := filepath.Join(dir, "clientrbac-"+ns+"-input.yaml")
	output := filepath.Join(dir, "clientrbac-"+ns+"-output.yaml")

	clientRbac := map[string]any{
		"create": true,
		"subjects": []any{
			map[string]any{"kind": "User", "name": "alice", "apiGroup": "rbac.authorization.k8s.io"},
		},
	}
	writeValuesFile(t, input, map[string]any{"clientRbac": clientRbac})

	_, stderr, err := s.CLI().Run(ctx,
		"setup", "--manager-namespace", ns, "--non-interactive", "--input", input, "--output", output)
	s.Require().NoError(err, "setup --input --output: %s", stderr)

	want := map[string]any{
		"create":       true,
		"legacyAccess": false,
		"subjects": []any{
			map[string]any{"kind": "User", "name": "alice", "apiGroup": "rbac.authorization.k8s.io"},
		},
	}
	values := loadValuesFile(t, output)
	s.Equal(want, values["clientRbac"])

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "no --apply means no release")
}

// Test_Validation checks the plain `setup --non-interactive` report against
// a fresh namespace: the findings section (render.go's printFindings), the
// proposed configuration, the planned-objects list (always computed for an
// ActionInstall proposal regardless of --apply; see cmd/setup.go's emit),
// the would-install action a validation-only run reports, and that a
// non-`yes` probe outcome (facts.go's Prober.Outcome) reaches the plain
// progress writer: the harness runs with TELEPRESENCE_PROGRESS=plain (see
// runtime.go), whose Warning events go to stderr (progress/plain.go).
// Nothing installs cert-manager on the test cluster, so the external
// endpoint phase's cert-manager finding is always "no".
func (s *Setup) Test_Validation() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "validate")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	dir := r.ArtifactDir("setup")
	values := filepath.Join(dir, "validate-"+ns+"-values.yaml")
	stdout, stderr, err := s.CLI().Run(ctx, "setup", "--manager-namespace", ns, "--non-interactive", "--output", values)
	s.Require().NoError(err, "setup: %s", stderr)

	for _, want := range []string{
		"Findings:",
		"cluster: context ",
		"release: not installed",
		"node-agent: yes",
		"  routing: ",
		"Proposed configuration:",
		"  quicTunnel:\n    enabled: true",
		"This install will create:",
		"removed by 'telepresence helm uninstall'",
		"Action: would-install",
	} {
		s.Contains(stdout, want)
	}
	s.Contains(stdout, "cert-manager no, 0 TLS secrets",
		"the external-endpoint phase's outcome must reach the plain progress writer")

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "validation-only run must not create a release")
}

// connectIdentity is the --as value for the ServiceAccount clientRbac grants
// below: the same identity name the auth suites use (managers.
// TestServiceAccount), though the connecting client here talks to its own
// private release rather than the shared one.
const connectIdentity = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount

// Test_SecurityInput installs with security.authentication.mode enforcing,
// security.authorization.requiredGrant telepresence, and clientRbac.legacyAccess
// false, with the agent-injector and node-agent both disabled so no attach
// machinery is provisioned, then connects under the granted identity to prove
// the resulting installation actually admits a client. The rendered connect
// Role (charts/telepresence-oss/templates/clientRbac/connect.yaml) is checked
// against what legacyAccess false and requiredGrant telepresence actually
// render: pods/portforward scoped to the known pod name rather than the
// legacy unscoped/discovery form, and the telepresence.io connections grant.
func (s *Setup) Test_SecurityInput() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "security")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	dir := r.ArtifactDir("setup")
	input := filepath.Join(dir, "security-"+ns+"-input.yaml")
	output := filepath.Join(dir, "security-"+ns+"-output.yaml")
	writeValuesFile(t, input, map[string]any{
		"security": map[string]any{
			"authentication": map[string]any{"mode": "enforcing"},
			"authorization":  map[string]any{"requiredGrant": "telepresence"},
		},
		"clientRbac": map[string]any{
			"legacyAccess": false,
			"create":       true,
			"subjects": []any{
				map[string]any{"kind": "ServiceAccount", "name": managers.TestServiceAccount, "namespace": managers.ManagerNamespace},
			},
		},
		"agentInjector": map[string]any{"enabled": false},
		"nodeAgent":     map[string]any{"enabled": false},
	})

	stdout, stderr, err := s.CLI().Run(ctx,
		"setup", "--manager-namespace", ns, "--non-interactive", "--input", input, "--output", output, "--apply")
	s.Require().NoError(err, "setup --apply: %s", stderr)
	s.Contains(stdout, "Action: install")

	values := loadValuesFile(t, output)
	mode, _ := valueAtPath(values, "security", "authentication", "mode")
	s.Equal("enforcing", mode)
	grant, _ := valueAtPath(values, "security", "authorization", "requiredGrant")
	s.Equal("telepresence", grant)
	legacy, _ := valueAtPath(values, "clientRbac", "legacyAccess")
	s.Equal(false, legacy)

	helmValues, err := helmGetValues(ctx, r, ns)
	s.Require().NoError(err)
	mode, _ = valueAtPath(helmValues, "security", "authentication", "mode")
	s.Equal("enforcing", mode)
	grant, _ = valueAtPath(helmValues, "security", "authorization", "requiredGrant")
	s.Equal("telepresence", grant)
	legacy, _ = valueAtPath(helmValues, "clientRbac", "legacyAccess")
	s.Equal(false, legacy)

	roleYAML, err := r.Kubectl(ctx, ns, "get", "role", "traffic-manager-connect", "-o", "yaml")
	s.Require().NoError(err, "reading the connect Role")
	var role rbac.Role
	s.Require().NoError(yaml.Unmarshal([]byte(roleYAML), &role))
	var scopedPortForward, legacyDiscovery, connectionsGrant bool
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			switch res {
			case "pods/portforward":
				if len(rule.ResourceNames) > 0 {
					scopedPortForward = true
				}
			case "pods", "services":
				legacyDiscovery = true
			case "connections":
				for _, g := range rule.APIGroups {
					if g == "telepresence.io" {
						connectionsGrant = true
					}
				}
			}
		}
	}
	s.True(scopedPortForward, "legacyAccess false should scope pods/portforward to the known pod name:\n%s", roleYAML)
	s.False(legacyDiscovery, "legacyAccess false should not render the legacy pods/services discovery rules:\n%s", roleYAML)
	s.True(connectionsGrant, "requiredGrant telepresence should grant telepresence.io connections:\n%s", roleYAML)

	if _, _, err := s.CLI().Run(ctx, "quit", "-s"); err != nil {
		t.Logf("[rtest] setup: quit -s before connect: %v", err)
	}
	r.ForgetConnections()
	t.Cleanup(func() {
		if _, _, err := s.CLI().Run(ctx, "quit", "-s"); err != nil {
			r.Infof("[rtest] setup: quit -s after Test_SecurityInput: %v", err)
		}
		r.ForgetConnections()
	})

	_, stderr, err = s.CLI().Run(ctx, "connect", "--namespace", ns, "--manager-namespace", ns, "--as", connectIdentity)
	s.Require().NoError(err, "connect: %s", stderr)

	var st cli.Status
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.True(st.UserDaemon.Running, "the user daemon should report connected")
	s.Equal(ns, st.UserDaemon.ManagerNamespace)
}

// Test_ExternalEndpointValidation checks ValidateValues's two chart guards
// (input.go): externalEndpoint.enabled requires security.authentication.mode
// enforcing, and its tls block must set exactly one of secretName or
// certManager.enabled. Both are hard errors before any release is touched.
func (s *Setup) Test_ExternalEndpointValidation() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "extvalidate")
	t.Cleanup(func() { uninstallIfPresent(t, ctx, r, ns) })

	dir := r.ArtifactDir("setup")

	t.Run("externalEndpoint without enforcing mode", func(t *testing.T) {
		input := filepath.Join(dir, "extvalidate-"+ns+"-permissive-input.yaml")
		output := filepath.Join(dir, "extvalidate-"+ns+"-permissive-output.yaml")
		writeValuesFile(t, input, map[string]any{
			"security":         map[string]any{"authentication": map[string]any{"mode": "permissive"}},
			"externalEndpoint": map[string]any{"enabled": true},
		})
		_, stderr, err := s.CLI().Run(ctx,
			"setup", "--manager-namespace", ns, "--non-interactive", "--input", input, "--output", output)
		s.Require().Error(err)
		s.Contains(stderr, "externalEndpoint.enabled requires security.authentication.mode: enforcing")
	})

	t.Run("externalEndpoint with no certificate source", func(t *testing.T) {
		input := filepath.Join(dir, "extvalidate-"+ns+"-notls-input.yaml")
		output := filepath.Join(dir, "extvalidate-"+ns+"-notls-output.yaml")
		writeValuesFile(t, input, map[string]any{
			"security":         map[string]any{"authentication": map[string]any{"mode": "enforcing"}},
			"externalEndpoint": map[string]any{"enabled": true},
		})
		_, stderr, err := s.CLI().Run(ctx,
			"setup", "--manager-namespace", ns, "--non-interactive", "--input", input, "--output", output)
		s.Require().Error(err)
		s.Contains(stderr, "set exactly one of tls.secretName or tls.certManager.enabled")
	})

	exists, err := releaseExists(ctx, r, ns)
	s.Require().NoError(err)
	s.False(exists, "neither validation failure may create a release")
}
