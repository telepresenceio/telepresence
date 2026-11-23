//go:build !windows

package connect

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// WshardRootLog proves that a root daemon started with sudo does not follow
// a link in the user-writable log directory.
type WshardRootLog struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardRootLog{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Sudo),
	)
}

// wshardFileState is what the test compares before and after the connect.
type wshardFileState struct {
	content string
	mode    os.FileMode
	uid     uint32
	gid     uint32
}

func wshardReadState(t testing.TB, path string) wshardFileState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st := wshardFileState{content: string(data), mode: fi.Mode()}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("cannot read the owner of %s: unexpected %T", path, fi.Sys())
	}
	st.uid, st.gid = sys.Uid, sys.Gid
	return st
}

// Test_LinkedLogFileIsNotFollowed replaces the root daemon's log file with a
// link to a marker file outside the log directory, connects, and asserts that
// the marker's content, mode and owner are unchanged.
func (s *WshardRootLog) Test_LinkedLogFileIsNotFollowed() {
	t := s.T()
	r := s.R()
	ns := s.AppNamespace()

	freeDefaultConnection(t, ns)

	logPath := filepath.Join(r.LogDir(), "daemon.log")
	saved := logPath + ".wshard-saved"
	if _, err := os.Lstat(logPath); err == nil {
		s.Require().NoError(os.Rename(logPath, saved))
	}

	marker := filepath.Join(t.TempDir(), "marker")
	s.Require().NoError(os.WriteFile(marker, []byte("wshard-marker\n"), 0o640))
	s.Require().NoError(os.Chmod(marker, 0o640))
	before := wshardReadState(t, marker)
	s.Require().NoError(os.Symlink(marker, logPath))

	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
			r.Infof("[rtest] WshardRootLog: quit -s: %v", err)
		}
		r.ForgetConnections()
		_ = os.Remove(logPath)
		if _, err := os.Lstat(saved); err == nil {
			_ = os.Rename(saved, logPath)
		}
	})

	ctx, cancel := context.WithTimeout(s.Ctx(), 90*time.Second)
	defer cancel()
	args := []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs}
	_, stderr, err := r.CLI().Run(ctx, args...)
	t.Logf("connect with a linked root daemon log: err=%v stderr=%s", err, stderr)

	// Stop the daemons so that nothing is written after the check.
	if _, _, err = r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
		t.Logf("quit -s: %v", err)
	}

	after := wshardReadState(t, marker)
	s.Equal(before.content, after.content, "the content of the file behind the link")
	s.Equal(before.mode, after.mode, "the mode of the file behind the link")
	s.Equal(before.uid, after.uid, "the owner of the file behind the link")
	s.Equal(before.gid, after.gid, "the group of the file behind the link")
}
