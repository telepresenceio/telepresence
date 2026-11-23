//go:build linux

package connect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// WshardDaemonPorts proves that the daemons choose their own ports: no port is
// fixed on their command lines, and the port in each info file is one that
// the daemon listens on.
type WshardDaemonPorts struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardDaemonPorts{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.On("linux"),
	)
}

// wshardDaemonProc is a running daemon of the binary under test.
type wshardDaemonProc struct {
	pid  int
	kind string
	addr string
}

// wshardDaemons lists the running user and root daemons of the binary exe
// that write their log in logDir, together with the value of their --address
// flag. Daemons that belong to other connections or to the system service
// have another --logfile and are not listed.
func wshardDaemons(t testing.TB, exe, logDir string) []wshardDaemonProc {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	var procs []wshardDaemonProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if len(argv) < 2 || filepath.Base(argv[0]) != filepath.Base(exe) || (argv[1] != "userd" && argv[1] != "rootd") {
			continue
		}
		p := wshardDaemonProc{pid: pid, kind: argv[1]}
		ours := false
		for i, a := range argv {
			logFile := ""
			if a == "--logfile" && i+1 < len(argv) {
				logFile = argv[i+1]
			} else if v, ok := strings.CutPrefix(a, "--logfile="); ok {
				logFile = v
			}
			if logFile != "" && filepath.Dir(logFile) == filepath.Clean(logDir) {
				ours = true
			}
			if a == "--address" && i+1 < len(argv) {
				p.addr = argv[i+1]
			} else if v, ok := strings.CutPrefix(a, "--address="); ok {
				p.addr = v
			}
		}
		if ours && p.addr != "" {
			procs = append(procs, p)
		}
	}
	return procs
}

// wshardListenInodes maps the local port of every listening TCP socket to its inode.
func wshardListenInodes(t testing.TB) map[int]string {
	t.Helper()
	ports := map[int]string{}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil {
				continue
			}
			ports[int(port)] = fields[9]
		}
		f.Close()
	}
	return ports
}

// wshardOwnsSocket reports whether the process pid has the socket inode open.
func wshardOwnsSocket(pid int, inode string) bool {
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	want := "socket:[" + inode + "]"
	for _, e := range entries {
		if l, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil && l == want {
			return true
		}
	}
	return false
}

// wshardInfoPort reads the daemon_port from an info file.
func wshardInfoPort(t testing.TB, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var info struct {
		DaemonPort int `json:"daemon_port"`
	}
	if err = json.Unmarshal(data, &info); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return info.DaemonPort
}

// Test_PortsAreChosenByTheDaemons asserts that every running user and root
// daemon was told to listen on port 0, and that the port in its info file is
// open: owned by the daemon itself for the user daemon, and accepting
// connections for the root daemon, whose descriptors the test cannot read.
func (s *WshardDaemonPorts) Test_PortsAreChosenByTheDaemons() {
	t := s.T()
	r := s.R()
	s.Connect()

	procs := wshardDaemons(t, r.Exe(), r.LogDir())
	var sawUser, sawRoot bool
	for _, p := range procs {
		s.True(strings.HasSuffix(p.addr, ":0"), "%s daemon (pid %d) has a fixed address %q on its command line", p.kind, p.pid, p.addr)
		switch p.kind {
		case "userd":
			sawUser = true
		case "rootd":
			sawRoot = true
		}
	}
	s.Require().True(sawUser, "no user daemon found among the processes")

	userPort := wshardInfoPort(t, filepath.Join(r.UserCacheDir(), "userd", "daemon.json"))
	s.Require().NotZero(userPort, "the user daemon's info file names no port")
	inode, listening := wshardListenInodes(t)[userPort]
	s.Require().True(listening, "nothing listens on the user daemon's port %d", userPort)
	owned := false
	for _, p := range procs {
		if p.kind == "userd" && wshardOwnsSocket(p.pid, inode) {
			owned = true
		}
	}
	s.True(owned, "the user daemon does not own the socket that listens on port %d", userPort)

	if !sawRoot {
		t.Log("no per-user root daemon process: the root daemon is managed by the system")
		return
	}
	rootPort := wshardInfoPort(t, filepath.Join(r.UserCacheDir(), "rootd", "daemon.json"))
	s.Require().NotZero(rootPort, "the root daemon's info file names no port")
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(rootPort)), 5*time.Second)
	if s.NoError(err, "the root daemon's port %d", rootPort) {
		_ = c.Close()
	}
}
