package connect

import (
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// ConnectErrors proves that connect fails clearly on bad input: an
// unparsable kubeconfig, a context that doesn't exist in it, and a
// namespace that exists but isn't managed by the traffic-manager. Needs the
// manager (and its RBAC) for the last case, since it must actually reach
// the manager to be told the namespace isn't managed.
type ConnectErrors struct {
	rt.Suite
}

func init() {
	rt.Register(&ConnectErrors{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// Test_InvalidKubeconfig points KUBECONFIG at unparsable content and expects
// connect to fail before ever reaching a daemon.
func (s *ConnectErrors) Test_InvalidKubeconfig() {
	path := filepath.Join(s.R().ArtifactDir("kubeconfig"), "garbage.yaml")
	s.Require().NoError(os.WriteFile(path, []byte("this is not yaml: [unterminated"), 0o600))

	_, stderr, err := s.R().CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(s.Ctx(), "connect")
	s.Error(err, "connect with an invalid kubeconfig should fail")
	s.Contains(strings.ToLower(stderr), "error loading config file")
}

// Test_NonExistentContext points the kubeconfig's current-context at a name
// that isn't defined and expects connect to fail with a clear message.
func (s *ConnectErrors) Test_NonExistentContext() {
	t := s.T()
	env := rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		cfg.CurrentContext = "no-such-context"
	})
	s.Require().NoError(err)

	_, stderr, err := s.R().CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(s.Ctx(), "connect")
	s.Error(err, "connect with a nonexistent context should fail")
	s.Contains(strings.ToLower(stderr), "context was not found")
}

// Test_UnmanagedNamespace creates a plain namespace (no managed label, via
// plain kubectl) and expects `connect --namespace` to it to fail, mentioning
// that the namespace isn't managed.
func (s *ConnectErrors) Test_UnmanagedNamespace() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()

	ns := "rtest-unmanaged-" + randSuffix()
	_, err := s.R().Kubectl(ctx, "", "create", "namespace", ns)
	s.Require().NoError(err)
	t.Cleanup(func() {
		_, _ = s.R().Kubectl(ctx, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
	})

	// This attempt reaches a real daemon (a valid kubeconfig, just a
	// different, unmanaged namespace): free the singleton host daemon slot
	// first, see freeDefaultConnection's doc comment.
	freeDefaultConnection(t, s.AppNamespace())
	t.Cleanup(func() {
		if _, _, err := s.CLI().Run(ctx, "quit", "-s"); err != nil {
			s.R().Infof("[rtest] Test_UnmanagedNamespace: quit -s: %v", err)
		}
		s.R().ForgetConnections()
	})

	args := []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs}
	_, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "connect to an unmanaged namespace should fail")
	s.Contains(strings.ToLower(stderr), "not managed")
}
