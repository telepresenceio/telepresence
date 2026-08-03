// Package cli wraps invocations of the telepresence binary under test.
package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TP wraps the binary under test: env-scrubbed subprocess, stdout/stderr
// capture.
type TP struct {
	Exe  string
	Env  []string
	Dir  string
	Logf func(string, ...any)
}

// defaultInvocationTimeout bounds a single CLI invocation (Run), or a
// Start...Wait pair's whole lifetime, when the caller's context has no
// deadline of its own. The CLI's internal timeouts normally fire well
// before this; the bound exists so a wedged daemon turns into a fast test
// failure instead of stalling the run until go test's timeout.
const defaultInvocationTimeout = 2 * time.Minute

// Run executes the binary with args and returns its captured stdout and
// stderr.
func (tp *TP) Run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultInvocationTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, tp.Exe, args...)
	cmd.Env = tp.Env
	cmd.Dir = tp.Dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if tp.Logf != nil {
		tp.Logf("+ %s %s", tp.Exe, strings.Join(args, " "))
	}
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// Proc is a background CLI invocation started by Start: its stdout/stderr
// are captured into buffers as it runs, exactly like Run's, but Start
// returns as soon as the process has launched instead of waiting for it to
// exit. Signal delivers a signal to it; Wait blocks for completion.
type Proc struct {
	cmd    *exec.Cmd
	outBuf *bytes.Buffer
	errBuf *bytes.Buffer
	done   chan error
	cancel context.CancelFunc
}

// Start begins the binary with args and returns once it has launched,
// without waiting for it to exit: for an invocation a test needs to signal
// or otherwise interact with while it keeps running, e.g. an `intercept
// --docker-run` handed off to a container and torn down by SIGINT/detach/
// disconnect/quit.
// Mirrors Run's env/dir/logging and defaultInvocationTimeout safety net;
// the timeout bounds the whole Start-to-Wait lifetime (released by Wait),
// so a Proc nobody ever waits on still gets killed instead of leaking.
func (tp *TP) Start(ctx context.Context, args ...string) (*Proc, error) {
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); ok {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, defaultInvocationTimeout)
	}
	cmd := exec.CommandContext(ctx, tp.Exe, args...)
	cmd.Env = tp.Env
	cmd.Dir = tp.Dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if tp.Logf != nil {
		tp.Logf("+ %s %s &", tp.Exe, strings.Join(args, " "))
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p := &Proc{cmd: cmd, outBuf: &outBuf, errBuf: &errBuf, done: make(chan error, 1), cancel: cancel}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}

// Signal delivers sig to the running process.
func (p *Proc) Signal(sig os.Signal) error {
	return p.cmd.Process.Signal(sig)
}

// Wait blocks until the process exits, or kills it once timeout elapses; it
// always releases the context Start armed, either way. It returns the
// process's captured stdout/stderr; err is the process's own exit error
// (nil on a clean exit), or a "did not exit" error naming timeout when Wait
// had to kill it.
func (p *Proc) Wait(timeout time.Duration) (stdout, stderr string, err error) {
	defer p.cancel()
	select {
	case err = <-p.done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		err = fmt.Errorf("cli: %s: did not exit within %s", p.cmd.Path, timeout)
	}
	return p.outBuf.String(), p.errBuf.String(), err
}

// OK runs the binary and requires it to succeed. A single-line "Warning:" or
// deprecation notice on stderr is tolerated (and logged); anything else on
// stderr fails the test, matching the old TelepresenceOk behavior.
func (tp *TP) OK(t testing.TB, args ...string) string {
	t.Helper()
	stdout, stderr, err := tp.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("%s %s: %v\nstderr:\n%s", tp.Exe, strings.Join(args, " "), err, stderr)
	}
	if stderr != "" {
		tolerated := !strings.ContainsRune(stderr, '\n') &&
			(strings.HasPrefix(stderr, "Warning:") || strings.Contains(stderr, "has been deprecated"))
		if tolerated {
			if tp.Logf != nil {
				tp.Logf("%s", stderr)
			}
		} else {
			t.Fatalf("%s %s: expected stderr to be empty, but got:\n%s", tp.Exe, strings.Join(args, " "), stderr)
		}
	}
	return stdout
}

// JSON runs the binary and unmarshals its stdout into out. Callers pass
// whichever of --output json / --format json the invoked command supports.
func (tp *TP) JSON(ctx context.Context, out any, args ...string) error {
	stdout, stderr, err := tp.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w\nstderr:\n%s", tp.Exe, strings.Join(args, " "), err, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return fmt.Errorf("%s %s: unmarshal output: %w\noutput:\n%s", tp.Exe, strings.Join(args, " "), err, stdout)
	}
	return nil
}
