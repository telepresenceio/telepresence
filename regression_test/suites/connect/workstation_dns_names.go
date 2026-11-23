package connect

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	wshardValidSuffix   = "wshard-valid.internal"
	wshardEscapeName    = "wshard-escape"
	wshardEscapeFile    = "/etc/" + wshardEscapeName
	wshardResolverGlob  = "/etc/resolver/*" + wshardValidSuffix + "*"
	wshardResolverPause = 15 * time.Second
)

// wshardHostileSuffixes holds one valid DNS suffix and names that are not
// DNS names but form file paths.
//
//nolint:gochecknoglobals // constant list
var wshardHostileSuffixes = []string{
	wshardValidSuffix,
	"../" + wshardEscapeName,
	"x/../../" + wshardEscapeName,
	"wshard/slash.internal",
	"wshard bad.internal",
}

// wshardRootStatus is the subset of `status --format json` that these suites inspect.
type wshardRootStatus struct {
	RootDaemon struct {
		DNS struct {
			IncludeSuffixes []string `json:"include_suffixes"`
		} `json:"dns"`
	} `json:"root_daemon"`
}

// WshardDNSNames proves that DNS suffixes that a local user gives through the
// kubeconfig's telepresence.io extension are used only when they are valid DNS
// names.
type WshardDNSNames struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardDNSNames{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// wshardConnectWithSuffixes connects through a kubeconfig whose extension
// lists suffixes, and quits the daemons when the test ends.
func wshardConnectWithSuffixes(s *rt.Suite, suffixes []string) {
	t := s.T()
	ctx := s.Ctx()
	r := rt.R()
	ns := s.AppNamespace()

	path, err := rt.WithKubeConfigExtension(rt.Env{Ctx: ctx, T: t, R: r}, map[string]any{
		"dns": map[string]any{"include-suffixes": suffixes},
	})
	if err != nil {
		t.Fatal(err)
	}
	freeDefaultConnection(t, ns)
	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
			r.Infof("[rtest] Wshard DNS names: quit -s: %v", err)
		}
		r.ForgetConnections()
	})
	args := []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs}
	if _, stderr, err := r.CLIWithEnv(map[string]string{"KUBECONFIG": path}).Run(ctx, args...); err != nil {
		t.Fatalf("connect: %v: %s", err, stderr)
	}
}

// Test_InvalidSuffixesAreDropped asserts that the root daemon keeps the valid
// suffix and none of the others.
func (s *WshardDNSNames) Test_InvalidSuffixesAreDropped() {
	wshardConnectWithSuffixes(&s.Suite, wshardHostileSuffixes)

	var st wshardRootStatus
	s.Require().NoError(s.CLI().JSON(s.Ctx(), &st, "status", "--format", "json"))
	s.Contains(st.RootDaemon.DNS.IncludeSuffixes, wshardValidSuffix, "the valid suffix must survive")
	for _, name := range wshardHostileSuffixes[1:] {
		s.NotContains(st.RootDaemon.DNS.IncludeSuffixes, name, "invalid DNS name %q must be dropped", name)
	}
}

// WshardResolverFiles proves that the DNS suffixes never decide where the root
// daemon writes resolver files.
type WshardResolverFiles struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardResolverFiles{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.On("darwin"),
		rt.Requires(rt.Sudo),
	)
}

// Test_NoFileOutsideTheResolverDirectory connects with a suffix that names a
// file in /etc, and asserts that the file is not created while the valid
// suffix in the same list gets its resolver file.
func (s *WshardResolverFiles) Test_NoFileOutsideTheResolverDirectory() {
	t := s.T()
	_, err := os.Lstat(wshardEscapeFile)
	if err == nil {
		t.Skipf("%s exists", wshardEscapeFile)
	}
	s.Require().ErrorIs(err, fs.ErrNotExist, "cannot confirm that %s is absent", wshardEscapeFile)
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "rm", "-f", wshardEscapeFile).Run()
	})
	wshardConnectWithSuffixes(&s.Suite, wshardHostileSuffixes)

	s.Eventually(func() bool {
		m, _ := filepath.Glob(wshardResolverGlob)
		return len(m) > 0
	}, wshardResolverPause, time.Second, "the valid suffix must get a resolver file")
	_, err = os.Lstat(wshardEscapeFile)
	s.ErrorIs(err, fs.ErrNotExist, "%s must not be created: %v", wshardEscapeFile, err)
}
