//go:build !windows

package connect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// WshardManagedFiles proves that the client ignores files at the locations
// that belong to the administrator when someone else owns them.
type WshardManagedFiles struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardManagedFiles{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.On("linux", "darwin"),
		rt.Requires(rt.Sudo),
	)
}

func wshardSudo(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "sudo", append([]string{"-n"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo %v: %w: %s", args, err, out)
	}
	return nil
}

// wshardPlantFile creates path, owned by the current user, in directories that
// the administrator owns, and removes it and any directory it had to create
// when the test ends. The test is skipped if path exists, and fails if its
// absence cannot be confirmed. The file is created exclusively, and removal is
// registered only for what this call created.
func wshardPlantFile(t *testing.T, path, content string) {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		t.Skipf("%s exists: it belongs to an installation that this test must not touch", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cannot confirm that %s is absent: %v", path, err)
	}
	ctx := context.Background()
	var created []string
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		_, err := os.Lstat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("cannot inspect %s: %v", d, err)
		}
		created = append(created, d)
	}
	// Directories are removed only if empty, so a directory that appeared
	// meanwhile and holds files survives.
	t.Cleanup(func() {
		for _, d := range created {
			_ = wshardSudo(ctx, "rmdir", d)
		}
	})
	if err := wshardSudo(ctx, "mkdir", "-p", filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	// noclobber makes the redirection fail if path exists.
	if err := wshardSudo(ctx, "sh", "-c", `set -C; : > "$1"`, "sh", path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = wshardSudo(ctx, "rm", "-f", path)
	})
	if err := wshardSudo(ctx, "chown", strconv.Itoa(os.Getuid()), path); err != nil {
		t.Fatal(err)
	}
	if err := wshardSudo(ctx, "chmod", "0644", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Test_UserOwnedRootDaemonInfoIsIgnored plants a root daemon info file, owned
// by the current user, in the root daemon's cache directory. It points at a
// listener of the test, and is kept fresh. A connect must not dial that
// listener.
func (s *WshardManagedFiles) Test_UserOwnedRootDaemonInfoIsIgnored() {
	t := s.T()
	r := s.R()
	ns := s.AppNamespace()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	s.Require().NoError(err)
	defer l.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()

	freeDefaultConnection(t, ns)

	path := filepath.Join(filelocation.RootCacheDir, "rootd", "daemon.json")
	wshardPlantFile(t, path, fmt.Sprintf(`{"daemon_port":%d}`, l.Addr().(*net.TCPAddr).Port))

	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				_ = os.Chtimes(path, now, now)
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = wshardSudo(context.Background(), "rm", "-f", path)
		if _, _, err := r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
			r.Infof("[rtest] WshardManagedFiles: quit -s: %v", err)
		}
		r.ForgetConnections()
	})

	ctx, cancel := context.WithTimeout(s.Ctx(), 90*time.Second)
	defer cancel()
	args := []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs}
	_, stderr, err := r.CLI().Run(ctx, args...)
	s.NoError(err, "connect must ignore the planted info file and start its own root daemon: %s", stderr)
	s.Zero(accepted.Load(), "the client connected to the port in a root daemon info file that the administrator does not own")
}

// wshardPlantedSuffix is the DNS include suffix of the planted configuration.
const wshardPlantedSuffix = "wshard-planted.internal"

// Test_UserOwnedSystemConfigIsIgnored plants the machine-wide configuration
// file, owned by the current user, with a DNS include suffix, and asserts that
// the client's effective configuration does not contain it.
func (s *WshardManagedFiles) Test_UserOwnedSystemConfigIsIgnored() {
	t := s.T()
	ctx := s.Ctx()

	wshardPlantFile(t, filepath.Join(filelocation.RootConfigDir, "config.yml"),
		"dns:\n  includeSuffixes:\n  - "+wshardPlantedSuffix+"\n")

	var cfg struct {
		ClientConfig struct {
			DNS struct {
				IncludeSuffixes []string `json:"includeSuffixes"`
			} `json:"dns"`
		} `json:"clientConfig"`
		ClientFile string `json:"clientFile"`
	}
	s.Require().NoError(s.CLI().JSON(ctx, &cfg, "config", "view", "--client-only", "--format", "json"))
	s.NotEmpty(cfg.ClientFile, "config view must report the client's own configuration file")
	s.NotContains(cfg.ClientConfig.DNS.IncludeSuffixes, wshardPlantedSuffix)
}
