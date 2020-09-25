// +build !windows

package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// GuessRunAsInfo attempts to construct a RunAsInfo for the user logged in at
// the primary display
func GuessRunAsInfo(p *supervisor.Process) (*RunAsInfo, error) {
	res := RunAsInfo{}
	if runtime.GOOS != "linux" {
		return &res, nil
	}
	pidDirs, err := ioutil.ReadDir("/proc")
	if err != nil {
		return nil, errors.Wrap(err, "read /proc")
	}
	for _, fi := range pidDirs {
		if !fi.IsDir() { // Skip /proc files
			continue
		}
		if fi.Sys().(*syscall.Stat_t).Uid == 0 { // Skip root processes
			continue
		}
		// Read the command line for this proc
		cmdline, err := ioutil.ReadFile("/proc/" + fi.Name() + "/cmdline")
		if err != nil {
			p.Logf("Guess/cmdline: Skipping %q: %v", fi.Name(), err)
			continue
		}
		// Skip programs that are not X
		args := bytes.FieldsFunc(cmdline, func(r rune) bool { return r == 0 || r == 32 })
		if len(args) == 0 || !bytes.ContainsRune(args[0], 'X') {
			continue
		}
		p.Logf("Guess: Trying env info from: %q", args[0])
		// Capture the environment for this proc
		environBlob, err := ioutil.ReadFile("/proc/" + fi.Name() + "/environ")
		if err != nil {
			p.Logf("Guess/environ: Skipping %q: %v", fi.Name(), err)
			continue
		}
		environBytes := bytes.Split(environBlob, []byte{0})
		environ := make([]string, len(environBytes))
		display := ""
		for idx := 0; idx < len(environBytes); idx++ {
			entry := string(environBytes[idx])
			environ[idx] = entry
			switch {
			case strings.HasPrefix(entry, "USER="):
				res.Name = entry[5:]
			case strings.HasPrefix(entry, "HOME="):
				res.Cwd = entry[5:]
			case strings.HasPrefix(entry, "DISPLAY="):
				display = entry[8:]
			}
		}
		if len(display) == 0 {
			display = os.Getenv("DISPLAY")
			if len(display) > 0 {
				environ = append(environ, fmt.Sprintf("DISPLAY=%s", display))
			}
		}
		res.Env = environ
		break
	}
	if len(res.Env) == 0 {
		return nil, errors.New("Guess: X server process not found")
	}
	if len(res.Cwd) == 0 || len(res.Name) == 0 {
		return nil, errors.New("Guess: Valid USER/HOME not found")
	}
	return &res, nil
}

// GetFreePort asks the kernel for a free open port that is ready to use.
// Similar to telepresence.utilities.find_free_port()
func GetFreePort() (int, error) {
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var operr error
			fn := func(fd uintptr) {
				operr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			}
			if err := c.Control(fn); err != nil {
				return err
			}
			return operr
		},
	}
	l, err := lc.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
