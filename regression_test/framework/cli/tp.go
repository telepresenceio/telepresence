// Package cli wraps invocations of the telepresence binary under test.
package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
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

// defaultInvocationTimeout bounds a single CLI invocation when the caller's
// context has no deadline of its own. The CLI's internal timeouts normally
// fire well before this; the bound exists so a wedged daemon turns into a
// fast test failure instead of stalling the run until go test's timeout.
const defaultInvocationTimeout = 5 * time.Minute

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
