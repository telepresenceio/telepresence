package connect

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const kubeAuthTokenPkg = "github.com/telepresenceio/telepresence/v2/regression_test/framework/kubeauthtoken"

const (
	// refreshTokenTTL is the lifetime the plugin reports. The root daemon
	// refreshes one minute earlier, so it refreshes refreshTokenTTL-1m after
	// the plugin ran.
	refreshTokenTTL = 75 * time.Second

	// refreshDeadline bounds the wait for the refresh after connect. The
	// refresh is due about 15s after the first credential was fetched.
	refreshDeadline = 60 * time.Second
)

// countLogLines returns the number of lines in path, from offset on, that contain needle.
func countLogLines(t *testing.T, path string, offset int64, needle string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err = f.Seek(offset, 0); err != nil {
		t.Fatalf("seek %s: %v", path, err)
	}
	n := 0
	scn := bufio.NewScanner(f)
	for scn.Scan() {
		if strings.Contains(scn.Text(), needle) {
			n++
		}
	}
	return n
}

// Test_ExecCredentialHostRefresh connects through a kubeconfig whose
// credential plugin reports a token that expires in refreshTokenTTL, then
// keeps the root daemon busy until it has asked the user daemon for a fresh
// credential. By design it takes about 15-20s after the connect.
//
// The token is a real one for the test service account, so the connection
// must keep working after the root daemon has replaced its credential.
func (s *KubeAuth) Test_ExecCredentialHostRefresh() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("kubeauth-refresh"))

	dir := r.ArtifactDir("bin")
	bin := filepath.Join(dir, "k8stoken")
	if out, err := exec.Command("go", "build", "-o", bin, kubeAuthTokenPkg).CombinedOutput(); err != nil {
		s.Require().NoError(err, "go build %s: %s", kubeAuthTokenPkg, out)
	}
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "create", "token", managers.TestServiceAccount, "--duration", "30m")
	s.Require().NoError(err)
	tokenFile := filepath.Join(dir, "k8stoken.txt")
	s.Require().NoError(os.WriteFile(tokenFile, []byte(strings.TrimSpace(out)), 0o600))

	path, err := rt.KubeConfigCopy(rt.Env{Ctx: ctx, T: t, R: r}, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		authInfoName := cc.AuthInfo + "-refresh"
		cfg.AuthInfos[authInfoName] = &api.AuthInfo{
			Exec: &api.ExecConfig{
				Command:          bin,
				Args:             []string{tokenFile, fmt.Sprint(int(refreshTokenTTL.Seconds()))},
				APIVersion:       "client.authentication.k8s.io/v1beta1",
				InteractiveMode:  api.NeverExecInteractiveMode,
				StdinUnavailable: true,
			},
		}
		cc = cc.DeepCopy()
		cc.AuthInfo = authInfoName
		cfg.Contexts[execCredentialContext] = cc
		cfg.CurrentContext = execCredentialContext
	})
	s.Require().NoError(err)

	logPath := filepath.Join(r.LogDir(), "connector.log")
	logSize := logFileSize(t, logPath)

	freeDefaultConnection(t, ns)
	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
			r.Infof("[rtest] KubeAuth: quit -s: %v", err)
		}
		r.ForgetConnections()
	})

	// The token authenticates as the service account itself, so no --as.
	_, stderr, err := r.CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(ctx,
		"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace)
	s.Require().NoError(err, "connect: %s", stderr)

	afterConnect := countLogLines(t, logPath, logSize, hostCredentialRequestLine)
	s.Require().NotZero(afterConnect, "the user daemon never ran the plugin for the root daemon")

	// Each lookup of a name that no cache holds goes from the root daemon to
	// the traffic-manager, which makes the root daemon ask its token source
	// for a token. Once the refresh point has passed that source asks the
	// user daemon, which runs the plugin again.
	deadline := time.Now().Add(refreshDeadline)
	total := afterConnect
	for i := 0; total <= afterConnect; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("the root daemon did not ask for a fresh credential within %s of connecting "+
				"(%d %q lines in connector.log, none after the connect); the credential refresh is not happening",
				refreshDeadline, afterConnect, hostCredentialRequestLine)
		}
		_, _ = net.LookupHost(fmt.Sprintf("kubeauth-refresh-%d-%d.%s", time.Now().UnixNano(), i, ns))
		time.Sleep(time.Second)
		total = countLogLines(t, logPath, logSize, hostCredentialRequestLine)
	}
	s.GreaterOrEqual(total, 2, "expected the first credential and at least one refresh in connector.log")

	rt.RoutedToCluster(t, wl.ServiceURL())
}
