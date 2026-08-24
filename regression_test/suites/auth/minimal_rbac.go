package auth

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// minimalRBACRules grants pods/portforward create scoped to
// traffic-manager-0, plus the telepresence.io connect/attach grants -- no
// get/list on pods or services at all.
const minimalRBACRules = `  - apiGroups: [""]
    resources: ["pods/portforward"]
    resourceNames: ["traffic-manager-0"]
    verbs: ["create"]
  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    verbs: ["create", "get"]`

// buildTokenKubeconfig derives a kubeconfig whose current context
// authenticates as tok, keeping the original cluster entry and swapping
// only the AuthInfo.
func buildTokenKubeconfig(env rt.Env, name, tok string) (string, error) {
	env.T.Helper()
	var buildErr error
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		if cc == nil {
			buildErr = fmt.Errorf("current context %q not found in kubeconfig", cfg.CurrentContext)
			return
		}
		authInfoName := name + "-token"
		cfg.AuthInfos[authInfoName] = &api.AuthInfo{Token: tok}
		derived := cc.DeepCopy()
		derived.AuthInfo = authInfoName
		ctxName := name + "-ctx"
		cfg.Contexts[ctxName] = derived
		cfg.CurrentContext = ctxName
	})
	if err != nil {
		return "", err
	}
	if buildErr != nil {
		return "", buildErr
	}
	return path, nil
}

// MinimalRBAC proves that an identity holding only minimalRBACRules -- no
// list/get on pods or services -- can still run a real `telepresence
// connect`, dialing the known pod name directly rather than discovering it.
type MinimalRBAC struct {
	rt.Suite
}

func init() {
	rt.Register(&MinimalRBAC{}, rt.InArea("auth"), rt.NeedsManager(managers.Default))
}

func (s *MinimalRBAC) Test_MinimalRBACConnect() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-minimal-rbac", minimalRBACRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	path, err := buildTokenKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, name, tok)
	s.Require().NoError(err)

	// Raw connect under a foreign KUBECONFIG: start from a clean daemon
	// slot (mirrors errors.go's Test_UnmanagedNamespace) and leave nothing
	// running for a later suite to adopt.
	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "MinimalRBAC:Test_MinimalRBACConnect")

	args := []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace}
	_, stderr, err := r.CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(ctx, args...)
	s.Require().NoError(err, "connect with a minimal (known-name-only) RBAC identity should succeed: %s", stderr)
}
