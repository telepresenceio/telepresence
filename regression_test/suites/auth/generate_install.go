package auth

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/labels"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// GenerateInstall proves that the output of `rbac generate install` is
// enough for `telepresence helm install` by an identity that has no other
// rights. Each case installs its own release in a private namespace, so the
// shared release is never touched.
type GenerateInstall struct {
	rt.Suite
}

func init() {
	rt.Register(&GenerateInstall{}, rt.InArea("auth"), rt.NeedsManager(managers.Default))
}

// generateInstallSelectorLabel selects the namespace of a cluster-wide case.
// Any selector other than a single kubernetes.io/metadata.name expression
// makes the chart render its cluster-wide rules.
const generateInstallSelectorLabel = "rtest.telepresence.io/rbac-generate"

type generateInstallCase struct {
	spec        managers.Spec
	clusterWide bool
	lifecycle   bool
}

func (s *GenerateInstall) Test_Namespaced() {
	s.run(generateInstallCase{spec: managers.Default})
}

func (s *GenerateInstall) Test_ClusterWide() {
	s.run(generateInstallCase{spec: managers.Default, clusterWide: true})
}

func (s *GenerateInstall) Test_X509() {
	s.run(generateInstallCase{spec: managers.AuthEnforcing()})
}

func (s *GenerateInstall) Test_X509ClusterWide() {
	s.run(generateInstallCase{spec: managers.AuthEnforcing(), clusterWide: true})
}

func (s *GenerateInstall) Test_Lifecycle() {
	s.run(generateInstallCase{spec: managers.Default, lifecycle: true})
}

func (s *GenerateInstall) run(c generateInstallCase) {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	if !r.UsesBuiltVersions() {
		t.Skip("rbac generate install needs the built client and chart")
	}
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateUnmanagedNamespace(env, "rbac-gen")

	var selector *labels.Selector
	if c.clusterWide {
		_, err := r.Kubectl(ctx, "", "label", "namespace", ns, generateInstallSelectorLabel+"="+ns)
		s.Require().NoError(err)
		selector = &labels.Selector{MatchExpressions: []*labels.Requirement{{
			Key: generateInstallSelectorLabel, Operator: labels.OperatorIn, Values: []string{ns},
		}}}
	}
	valuesPath, err := rt.SecondaryManagerValues(env, c.spec, ns, selector)
	s.Require().NoError(err)

	const sa = "rbac-gen-installer"
	_, err = r.Kubectl(ctx, ns, "create", "serviceaccount", sa)
	s.Require().NoError(err)
	tok, err := r.Kubectl(ctx, ns, "create", "token", sa)
	s.Require().NoError(err)
	kubeconfig, err := buildTokenKubeconfig(env, sa, strings.TrimSpace(tok))
	s.Require().NoError(err)
	asInstaller := r.CLIWithEnv(map[string]string{"KUBECONFIG": kubeconfig})
	installName := "telepresence-install-" + ns

	t.Cleanup(func() {
		_, _, _ = r.CLI().Run(ctx, "helm", "uninstall", "--manager-namespace", ns)
		_, _ = r.Kubectl(ctx, "", "delete", "mutatingwebhookconfiguration", "agent-injector-webhook-"+ns, "--ignore-not-found")
		_, _ = r.Kubectl(ctx, "", "delete", "clusterrolebinding,clusterrole", installName, "--ignore-not-found")
		_, _ = r.Kubectl(ctx, "kube-system", "delete", "rolebinding,role", installName, "--ignore-not-found")
	})

	// The identity has no rights of its own.
	_, _, err = asInstaller.Run(ctx, "helm", "install", "-n", ns, "-f", valuesPath)
	s.Require().Error(err, "helm install without the generated RBAC should fail")

	verbs := "install"
	if c.lifecycle {
		verbs = "lifecycle"
	}
	generateArgs := []string{
		"rbac", "generate", "install", "--manager-namespace", ns, "--input", valuesPath, "--verbs", verbs,
		"--as", "system:serviceaccount:" + ns + ":" + sa,
	}
	stdout, stderr, err := r.CLI().Run(ctx, generateArgs...)
	s.Require().NoError(err, stderr)
	s.Require().Contains(stdout, "kind: ClusterRole")
	rbacPath := filepath.Join(t.TempDir(), "install-rbac.yaml")
	s.Require().NoError(os.WriteFile(rbacPath, []byte(stdout), 0o644))
	_, err = r.Kubectl(ctx, "", "apply", "-f", rbacPath)
	s.Require().NoError(err)

	_, stderr, err = asInstaller.Run(ctx, "helm", "install", "-n", ns, "-f", valuesPath)
	s.Require().NoError(err, "helm install with the generated RBAC should succeed: %s", stderr)

	stdout, stderr, err = r.CLI().Run(ctx, generateArgs...)
	s.Require().NoError(err, stderr)
	s.Contains(stdout, "There is nothing to grant")

	if c.lifecycle {
		_, stderr, err = asInstaller.Run(ctx, "helm", "upgrade", "-n", ns, "-f", valuesPath)
		s.Require().NoError(err, "helm upgrade with the generated RBAC should succeed: %s", stderr)
		_, stderr, err = asInstaller.Run(ctx, "helm", "uninstall", "-n", ns)
		s.Require().NoError(err, "helm uninstall with the generated RBAC should succeed: %s", stderr)
	}
}
