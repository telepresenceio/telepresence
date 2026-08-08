package auth

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// minimalRBACRules grants exactly the RBAC a phase-3, known-name-only
// client needs to connect: pods/portforward create scoped to the
// StatefulSet's known pod (traffic-manager-0) -- no get/list on pods or
// services at all, so the legacy Service-discovery path is not merely
// unused but actually forbidden -- plus the telepresence.io connect/attach
// grants authorizeConnect and authorizeAttachment review. Mirrors
// telepresenceGrantRules, adding the scoped mechanical portforward grant a
// real connect (as opposed to gate.go's direct ArriveAsClient calls) also
// needs.
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

// buildTokenKubeconfig derives a kubeconfig (rt.KubeConfigCopy) whose
// current context authenticates as tok instead of the run's own identity: a
// literal different-identity kubeconfig, as opposed to the framework's
// usual --as (which still dials out using the run's own credentials and
// relies on the API server's impersonation RBAC). The derived context keeps
// the original cluster entry (same server, same CA) and only swaps the
// AuthInfo.
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

// MinimalRBAC proves the client-rbac-minimization phase-3 contract at the
// RBAC boundary: an identity holding only minimalRBACRules -- nothing that
// would let it list or get pods/services -- can still run a real
// `telepresence connect`, because the client dials the StatefulSet's known
// pod name (traffic-manager-0, pkg/client/k8s/connect.go's
// ConnectToManager) directly rather than discovering it. Connects with a
// derived kubeconfig carrying the identity's own bearer token (see
// buildTokenKubeconfig), not --as: --as still authenticates as the run's
// own identity and merely asks the API server to impersonate another,
// which needs its own (broader) RBAC this test is not about.
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

	name := createGateIdentity(t, ctx, r, "rtest-auth-minimal-rbac", minimalRBACRules)
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
