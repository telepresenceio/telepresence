// Package dexec is a logging variant of os/exec.
//
// dexec is *almost* a drop-in replacement for os/exec.  Differences
// are:
//
// - The "Command" function is missing, because a context is always
// required; use CommandContext.
//
// - It is not valid to create a "Cmd" entirely by hand; you must
// create it using CommandContext.  After it has been created, you may
// adjust the fields as you would with an os/exec.Cmd.
//
// The logger used is configured in the context.Context passed to
// CommandContext by calling
// github.com/datawire/teleproxy/pkg/dlog.WithLogger.
//
// A Cmd logs when it starts, its exit status, and if they aren't an
// *os.File, logs everything read from or written to .Stdin, .Stdout,
// and .Stderr.  If one of those is an *os.File (as it is following a
// call to .StdinPipe, .StdoutPipe, or .StderrPipe), then that stream
// won't be logged (but it will print a message at process-start
// noting that it isn't being logged).
//
// For example:
//
//     ctx := dlog.WithLogger(context.Background(), myLogger)
//     cmd := dexec.CommandContext(ctx, "printf", "%s\n", "foo bar", "baz")
//     cmd.Stdin = os.Stdin
//     err := cmd.Run()
//
// will log the lines
//
//     [pid:24272] started command []string{"printf", "%s\n", "foo bar", "baz"}
//     [pid:24272] stdin  < not logging input read from file /dev/stdin
//     [pid:24272] stdout+stderr > "foo bar\n"
//     [pid:24272] stdout+stderr > "baz\n"
//     [pid:24272] finished successfully: exit status 0
//
// If you would like a "pipe" to be logged, use an io.Pipe instead of
// calling .StdinPipe, .StdoutPipe, or .StderrPipe.
//
// See the os/exec documentation for more information.
package dexec

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/datawire/teleproxy/pkg/dlog"
)

// Error is returned by LookPath when it fails to classify a file as an
// executable.
type Error = exec.Error

// An ExitError reports an unsuccessful exit by a command.
type ExitError = exec.ExitError

// ErrNotFound is the os/exec.ErrNotFound value.
var ErrNotFound = exec.ErrNotFound

// LookPath is the os/exe.LookPath function.
var LookPath = exec.LookPath

// Cmd represents an external command being prepared or run.
//
// A Cmd cannot be reused after calling its Run, Output or CombinedOutput
// methods.
//
// See the os/exec.Cmd documentation for information on the fields
// within it.
//
// Unlike an os/exec.Cmd, you MUST NOT construct a Cmd by hand, it
// must be created with CommandContext.
type Cmd struct {
	*exec.Cmd
	logger dlog.Logger

	pidlock sync.RWMutex
}

// CommandContext returns the Cmd struct to execute the named program with
// the given arguments.
//
// The provided context is used for two purposes:
//
//  1. To kill the process (by calling os.Process.Kill) if the context
//     becomes done before the command completes on its own.
//  2. To get the logger (by calling
//     github.com/datawire/teleproxy/pkg/dlog.GetLogger on it).
//
// See the os/exec.Command and os/exec.CommandContext documentation
// for more information.
func CommandContext(ctx context.Context, name string, arg ...string) *Cmd {
	ret := &Cmd{
		Cmd:    exec.CommandContext(ctx, name, arg...),
		logger: dlog.GetLogger(ctx),
	}
	ret.pidlock.Lock()
	return ret
}

func (c *Cmd) logiofn(prefix string) func(string) {
	return func(msg string) {
		c.pidlock.RLock()
		defer c.pidlock.RUnlock()
		pid := -1
		if c.Process != nil {
			pid = c.Process.Pid
		}
		c.logger.Printf("[pid:%v] %s %s", pid, prefix, msg)
	}
}

// Start starts the specified command but does not wait for it to complete.
//
// See the os/exec.Cmd.Start documenaton for more information.
func (c *Cmd) Start() error {
	c.Stdin = fixupReader(c.Stdin, c.logiofn("stdin  <"))
	if interfaceEqual(c.Stdout, c.Stderr) {
		c.Stdout = fixupWriter(c.Stdout, c.logiofn("stdout+stderr >"))
		c.Stderr = c.Stdout
	} else {
		c.Stdout = fixupWriter(c.Stdout, c.logiofn("stdout >"))
		c.Stderr = fixupWriter(c.Stderr, c.logiofn("stderr >"))
	}

	err := c.Cmd.Start()
	if err == nil {
		c.logger.Printf("[pid:%v] started command %#v", c.Process.Pid, c.Args)
		if stdin, isFile := c.Stdin.(*os.File); isFile {
			c.logger.Printf("[pid:%v] stdin  < not logging input read from file %s", c.Process.Pid, stdin.Name())
		}
		if stdout, isFile := c.Stdout.(*os.File); isFile {
			c.logger.Printf("[pid:%v] stdout > not logging output written to file %s", c.Process.Pid, stdout.Name())
		}
		if stderr, isFile := c.Stderr.(*os.File); isFile {
			c.logger.Printf("[pid:%v] stderr > not logging output written to file %s", c.Process.Pid, stderr.Name())
		}
	}
	c.pidlock.Unlock()
	return err
}

// Wait waits for the command to exit and waits for any copying to
// stdin or copying from stdout or stderr to complete.
//
// See the os/exec.Cmd.Wait documenaton for more information.
func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()

	pid := -1
	if c.Process != nil {
		pid = c.Process.Pid
	}

	if err == nil {
		c.logger.Printf("[pid:%v] finished successfully: %v", pid, c.ProcessState)
	} else {
		c.logger.Printf("[pid:%v] finished with error: %v", pid, err)
	}

	return err
}

// StdinPipe returns a pipe that will be connected to the command's
// standard input when the command starts.
//
// This sets .Stdin to an *os.File, causing what you write to the pipe
// to not be logged.
//
// See the os/exec.Cmd.StdinPipe documenaton for more information.
func (c *Cmd) StdinPipe() (io.WriteCloser, error) { return c.Cmd.StdinPipe() }

// StdoutPipe returns a pipe that will be connected to the command's
// standard output when the command starts.
//
// This sets .Stdout to an *os.File, causing what you read from the
// pipe to not be logged.
//
// See the os/exec.Cmd.StdoutPipe documenaton for more information.
func (c *Cmd) StdoutPipe() (io.ReadCloser, error) { return c.Cmd.StdoutPipe() }

// StderrPipe returns a pipe that will be connected to the command's
// standard error when the command starts.
//
// This sets .Stderr to an *os.File, causing what you read from the
// pipe to not be logged.
//
// See the os/exec.Cmd.StderrPipe documenaton for more information.
func (c *Cmd) StderrPipe() (io.ReadCloser, error) { return c.Cmd.StderrPipe() }

// Higher-level methods around these implemented in borrowed_cmd.go
