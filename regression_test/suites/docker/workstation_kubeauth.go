//go:build linux

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	wshardKubeAuthConn    = "rtest-wshard-kubeauth"
	wshardKubeAuthContext = "wshard-exec-context"
	wshardKubeAuthCreds   = "github.com/telepresenceio/telepresence/v2/regression_test/framework/kubeauthcreds"
	wshardConnectAs       = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount
)

// WshardKubeAuth proves that the token which the containerized daemon presents
// to the host's kubeauth service is not on the command line of any process.
type WshardKubeAuth struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardKubeAuth{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// wshardCmdlines returns the command line of every process, keyed by pid.
func wshardCmdlines() map[int][]string {
	entries, _ := os.ReadDir("/proc")
	out := make(map[int][]string, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		out[pid] = strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
	return out
}

// wshardStubWatcher records the command line of every process that runs the
// kubeconfig stub, the telepresence "kubeauth" command, while it is running.
type wshardStubWatcher struct {
	mu   sync.Mutex
	seen map[int][]string
	stop chan struct{}
	done chan struct{}
}

func wshardWatchStubs() *wshardStubWatcher {
	w := &wshardStubWatcher{seen: map[int][]string{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			for pid, argv := range wshardCmdlines() {
				if len(argv) > 1 && filepath.Base(argv[0]) == "telepresence" && argv[1] == "kubeauth" {
					w.mu.Lock()
					w.seen[pid] = argv
					w.mu.Unlock()
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return w
}

func (w *wshardStubWatcher) finish() map[int][]string {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

// wshardExecKubeconfig derives a kubeconfig whose current context gets its
// credentials from an exec plugin.
func wshardExecKubeconfig(env rt.Env, bin string) (string, error) {
	var buildErr error
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		orig := cfg.CurrentContext
		cc := cfg.Contexts[orig]
		if cc == nil {
			buildErr = fmt.Errorf("current context %q not found in kubeconfig", orig)
			return
		}
		authInfo := cc.AuthInfo + "-wshard-exec"
		cfg.AuthInfos[authInfo] = &api.AuthInfo{
			Exec: &api.ExecConfig{
				Command:          bin,
				Args:             []string{orig},
				APIVersion:       "client.authentication.k8s.io/v1beta1",
				InteractiveMode:  api.NeverExecInteractiveMode,
				StdinUnavailable: true,
			},
		}
		ec := cc.DeepCopy()
		ec.AuthInfo = authInfo
		cfg.Contexts[wshardKubeAuthContext] = ec
		cfg.CurrentContext = wshardKubeAuthContext
	})
	if err != nil {
		return "", err
	}
	return path, buildErr
}

// Test_TokenIsNotOnACommandLine connects through a containerized daemon with a
// kubeconfig that uses an exec plugin, watches the processes while the daemon
// starts and works, and asserts that the kubeauth token is on no command line
// and that the kubeconfig stub is not started with a token flag.
func (s *WshardKubeAuth) Test_TokenIsNotOnACommandLine() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	bin := filepath.Join(r.ArtifactDir("bin"), "wshard-k8screds")
	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, wshardKubeAuthCreds).CombinedOutput()
	s.Require().NoError(err, "go build: %s", out)
	path, err := wshardExecKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, bin)
	s.Require().NoError(err)

	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(context.Background(), "quit", "--use", wshardKubeAuthConn); err != nil {
			r.Infof("[rtest] WshardKubeAuth: quit: %v", err)
		}
	})
	if _, _, err = r.CLI().Run(ctx, "quit", "--use", wshardKubeAuthConn); err != nil {
		t.Logf("quit before connect: %v", err)
	}

	connectStart := time.Now()
	w := wshardWatchStubs()
	args := []string{
		"connect", "--docker", "--name", wshardKubeAuthConn,
		"--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", wshardConnectAs,
	}
	_, stderr, err := r.CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(ctx, args...)
	if err == nil {
		// Let the daemon in the container use its credentials for a while.
		for i := 0; i < 5; i++ {
			_, _, _ = r.CLI().Run(ctx, "list", "--use", wshardKubeAuthConn)
			time.Sleep(time.Second)
		}
	}
	stubs := w.finish()
	s.Require().NoError(err, "connect: %s", stderr)
	s.Require().NotEmpty(stubs, "the kubeconfig stub was not seen running, so its command line could not be checked")

	for pid, argv := range stubs {
		s.NotContains(argv, "--token", "the stub (pid %d) is started with a token flag: %v", pid, argv)
	}

	// The daemon names its port file after the kubeconfig list and context, so
	// the file this test's daemon wrote is the only kubeauthd-*.port file
	// written since the connect started.
	portFiles, err := filepath.Glob(filepath.Join(r.UserCacheDir(), "kubeauthd-*.port"))
	s.Require().NoError(err)
	var fresh []string
	for _, pf := range portFiles {
		if fi, err := os.Stat(pf); err == nil && !fi.ModTime().Before(connectStart) {
			fresh = append(fresh, pf)
		}
	}
	s.Require().Len(fresh, 1, "expected exactly one kubeauth port file written since the connect: %v", fresh)
	data, err := os.ReadFile(fresh[0])
	s.Require().NoError(err, "the kubeauth port file")
	var pf struct {
		Token string `json:"token"`
	}
	s.Require().NoError(json.Unmarshal(data, &pf))
	s.Require().NotEmpty(pf.Token, "the kubeauth port file holds no token")
	for pid, argv := range wshardCmdlines() {
		s.NotContains(strings.Join(argv, " "), pf.Token, "the token is on the command line of process %d", pid)
	}
	for pid, argv := range stubs {
		s.NotContains(strings.Join(argv, " "), pf.Token, "the token is on the command line of stub %d", pid)
	}
}
