package connect

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// execCredentialContext names the context the derived kubeconfig below adds:
// punctuation a shell or a naive identifier scheme would choke on, proving
// the client converts it to a usable identifier rather than merely accepting
// simple names.
const execCredentialContext = "abc:def/xyz$32-#1?efd"

// kubeAuthCredsPkg is the exec-credential helper program's import path (see
// regression_test/framework/kubeauthcreds): a kubeconfig `exec` plugin that
// wraps another context's own credentials, letting a test force the
// GetContextExecCredentials path without an external credential provider.
const kubeAuthCredsPkg = "github.com/telepresenceio/telepresence/v2/regression_test/framework/kubeauthcreds"

// KubeAuth proves that a kubeconfig user carrying an `exec` block is
// resolved through pkg/authenticator: pkg/authenticator/grpc/
// authenticator.go's AuthenticatorServer.GetContextExecCredentials logs
// "GetContextExecCredentials(<context>)" once it resolves the wrapped
// context's credentials, and that line must reach the log file of whichever
// process served it -- the connector for a host connection, or the
// kubeauth daemon (pkg/client/docker/kubeauth) for a docker one, since the
// containerized daemon cannot itself exec a host-side credential plugin.
type KubeAuth struct {
	rt.Suite
}

func init() {
	rt.Register(&KubeAuth{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// buildKubeAuthCredsBinary builds the exec-credential helper program into
// dir/k8screds (dir/k8screds.exe on windows) and returns its path.
func buildKubeAuthCredsBinary(dir string) (string, error) {
	bin := filepath.Join(dir, "k8screds")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, kubeAuthCredsPkg).CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build %s: %w: %s", kubeAuthCredsPkg, err, out)
	}
	return bin, nil
}

// buildExecKubeconfig derives a kubeconfig (rt.KubeConfigCopy) that adds an
// AuthInfo whose `exec` block invokes bin (the built kubeauthcreds helper,
// see buildKubeAuthCredsBinary) with the original current-context's name as
// its sole argument, so it wraps that context's own credentials, and a new
// context named execCredentialContext using that AuthInfo, made the derived
// file's current-context. No Env entries are set on the ExecConfig: every
// connection here starts a fresh daemon under the KUBECONFIG this test
// itself passes to the connect invocation, and pkg/authenticator/exec.go's
// ResolveExecConfig inherits that daemon's own environment before running
// the command.
func buildExecKubeconfig(env rt.Env, bin string) (string, error) {
	env.T.Helper()
	var buildErr error
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		origContext := cfg.CurrentContext
		cc := cfg.Contexts[origContext]
		if cc == nil {
			buildErr = fmt.Errorf("current context %q not found in kubeconfig", origContext)
			return
		}
		authInfoName := cc.AuthInfo + "-exec"
		cfg.AuthInfos[authInfoName] = &api.AuthInfo{
			Exec: &api.ExecConfig{
				Command:          bin,
				Args:             []string{origContext},
				APIVersion:       "client.authentication.k8s.io/v1beta1",
				InteractiveMode:  api.NeverExecInteractiveMode,
				StdinUnavailable: true,
			},
		}
		extCc := cc.DeepCopy()
		extCc.AuthInfo = authInfoName
		cfg.Contexts[execCredentialContext] = extCc
		cfg.CurrentContext = execCredentialContext
	})
	if err != nil {
		return "", err
	}
	if buildErr != nil {
		return "", buildErr
	}
	return path, nil
}

// logFileSize returns path's current size, or 0 if it doesn't exist yet
// (the daemon that would create it has never run).
func logFileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", path, err)
		}
		return 0
	}
	return st.Size()
}

// logContainsExecCredentials scans path from offset for a line containing
// "GetContextExecCredentials(<context>)", the message AuthenticatorServer.
// GetContextExecCredentials (pkg/authenticator/grpc/authenticator.go) logs
// once it resolves context's credentials.
func logContainsExecCredentials(t *testing.T, path string, offset int64, context string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			t.Fatalf("seek %s: %v", path, err)
		}
	}
	needle := "GetContextExecCredentials(" + context + ")"
	scn := bufio.NewScanner(f)
	for scn.Scan() {
		if strings.Contains(scn.Text(), needle) {
			return true
		}
	}
	return false
}

// connectAndScanForExecCredentials builds the kubeauthcreds binary and a
// derived kubeconfig (buildExecKubeconfig), raw-connects with extraArgs
// appended (e.g. "--docker") under a KUBECONFIG pointing at it, and asserts
// logName (under r.LogDir()) recorded GetContextExecCredentials resolving
// execCredentialContext's credentials. Mirrors errors.go's
// Test_UnmanagedNamespace: a raw connect under a foreign KUBECONFIG starts
// from a clean daemon slot, and its cleanup quits every daemon and forgets
// memoized connections so later suites never adopt this session.
func (s *KubeAuth) connectAndScanForExecCredentials(logName string, extraArgs ...string) {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	bin, err := buildKubeAuthCredsBinary(r.ArtifactDir("bin"))
	s.Require().NoError(err)
	path, err := buildExecKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, bin)
	s.Require().NoError(err)

	logPath := filepath.Join(r.LogDir(), logName)
	logSize := logFileSize(t, logPath)

	freeDefaultConnection(t, ns)
	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
			r.Infof("[rtest] KubeAuth: quit -s: %v", err)
		}
		r.ForgetConnections()
	})

	args := append([]string{
		"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs,
	}, extraArgs...)
	_, stderr, err := r.CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(ctx, args...)
	s.Require().NoError(err, "connect: %s", stderr)

	s.True(logContainsExecCredentials(t, logPath, logSize, execCredentialContext),
		"expected GetContextExecCredentials(%s) in %s", execCredentialContext, logName)
}

// Test_ExecCredentialHostConnect connects (host daemon) through a derived
// kubeconfig whose current context resolves via the kubeauthcreds exec
// plugin, and asserts connector.log recorded the connector resolving
// execCredentialContext's credentials.
func (s *KubeAuth) Test_ExecCredentialHostConnect() {
	s.connectAndScanForExecCredentials("connector.log")
}

// Test_ExecCredentialDockerConnect is Test_ExecCredentialHostConnect's
// docker counterpart: the containerized daemon cannot itself exec a
// host-side credential plugin, so a separate kubeauth daemon
// (pkg/client/docker/kubeauth) runs on the host and serves
// GetContextExecCredentials in its place, logging to kubeauth.log instead
// of connector.log.
func (s *KubeAuth) Test_ExecCredentialDockerConnect() {
	t := s.T()
	if runtime.GOOS != "linux" {
		t.Skipf("skipping: requires GOOS in linux, running on %s", runtime.GOOS)
	}
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("skipping: requires capability \"docker\"")
	}
	s.connectAndScanForExecCredentials("kubeauth.log", "--docker")
}
