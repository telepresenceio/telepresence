package auth

import (
	"context"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	namespaceReadTimeout = 2 * time.Minute
	namespaceReadPoll    = 2 * time.Second
	namespaceReadQuiet   = 6 * time.Second
)

// workloadAttachmentRules grants create and get on the attachment named
// workload only.
func workloadAttachmentRules(workload string) string {
	return `  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    resourceNames: ["` + workload + `"]
    verbs: ["create", "get"]`
}

// connectOnlyIdentity creates a ServiceAccount that holds the connect
// grant in the manager namespace (minimalRBACRules) and nothing in any
// other namespace, and returns a CLI running under a kubeconfig that
// authenticates as it.
func connectOnlyIdentity(t *testing.T, ctx context.Context, r *rt.Runtime, name string) *cli.TP {
	t.Helper()
	createGrantIdentity(t, ctx, r, name, minimalRBACRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	path, err := buildTokenKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, name, tok)
	if err != nil {
		t.Fatalf("building kubeconfig for %s: %v", name, err)
	}
	return r.CLIWithEnv(map[string]string{"KUBECONFIG": path})
}

// connectArgs is the argument list for a raw connect to ns.
func connectArgs(ns string) []string {
	return []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace}
}

// listNames returns the names of the workloads `telepresence list` shows in
// ns.
func listNames(ctx context.Context, tp *cli.TP, ns string) ([]string, error) {
	var entries []cli.ListEntry
	if err := tp.JSON(ctx, &entries, "list", "--format", "json", "-n", ns); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

// Test_ConnectedNamespaceWithoutGrantListsNothing connects with --namespace
// as an identity that holds the connect grant but no grant in that
// namespace: the list is empty and carries no error.
func (s *AuthEnforcing) Test_ConnectedNamespaceWithoutGrantListsNothing() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	s.Workload(workloads.Echo("auth-nsread-a"))

	tp := connectOnlyIdentity(t, ctx, r, "rtest-auth-nsread-nogrant")

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "AuthEnforcing:ConnectedNamespaceWithoutGrantListsNothing")

	_, stderr, err := tp.Run(ctx, connectArgs(ns)...)
	s.Require().NoError(err, "connect with only the connect grant should succeed: %s", stderr)

	s.Never(func() bool {
		names, err := listNames(ctx, tp, ns)
		return err != nil || len(names) > 0
	}, namespaceReadQuiet, namespaceReadPoll, "list in an ungranted namespace should be empty and error-free")
}

// Test_WorkloadScopedGrantSeesOnlyThatWorkload gives an identity create and
// get on the attachment of one workload (resourceNames) in a namespace with
// two: only that workload is listed, and the other cannot be intercepted.
func (s *AuthEnforcing) Test_WorkloadScopedGrantSeesOnlyThatWorkload() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	granted := s.Workload(workloads.Echo("auth-nsread-granted"))
	other := s.Workload(workloads.Echo("auth-nsread-other"))
	ls := s.LocalEcho()

	const name = "rtest-auth-nsread-scoped"
	tp := connectOnlyIdentity(t, ctx, r, name)
	grantInNamespace(t, ctx, r, name, ns, workloadAttachmentRules(granted.Name))

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "AuthEnforcing:WorkloadScopedGrantSeesOnlyThatWorkload")

	_, stderr, err := tp.Run(ctx, connectArgs(ns)...)
	s.Require().NoError(err, "connect: %s", stderr)

	s.Require().Eventually(func() bool {
		names, err := listNames(ctx, tp, ns)
		return err == nil && len(names) == 1 && names[0] == granted.Name
	}, namespaceReadTimeout, namespaceReadPoll, "list should show exactly %s", granted.Name)
	s.Never(func() bool {
		names, err := listNames(ctx, tp, ns)
		if err != nil {
			return false
		}
		for _, n := range names {
			if n == other.Name {
				return true
			}
		}
		return false
	}, namespaceReadQuiet, namespaceReadPoll, "list must not show %s", other.Name)

	// The identity holds no pods/portforward in ns, so a successful intercept
	// would need the QUIC transport. The refusal needs neither.
	args := append([]string{"intercept", other.Name, "--namespace", ns}, rt.ToLocal(ls, "http")()...)
	args = append(args, cli.MountFalse()()...)
	stdout, stderr, err := tp.Run(ctx, args...)
	t.Cleanup(func() { _, _, _ = tp.Run(ctx, "detach", other.Name, "-n", ns) })
	s.Require().Error(err, "intercept of %s should be refused", other.Name)
	s.NotEmpty(stdout + stderr)
}
