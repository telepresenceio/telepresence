package supervisor

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type logger struct {
	process   *Process
	emptyLine bool
}

func (l *logger) Log(prefix, line string) {
	if l.emptyLine {
		l.process.Log(prefix)
		l.emptyLine = false
	}

	if line == "" {
		l.emptyLine = true
	} else {
		l.process.Logf("%s%s", prefix, line)
		l.emptyLine = false
	}
}

func (l *logger) LogLines(prefix, str string, err error) {
	if strings.HasSuffix(str, "\n") {
		str = str[:len(str)-1]
	} else {
		str += "\\no newline"
	}
	lines := strings.Split(str, "\n")
	for _, line := range lines {
		l.Log(prefix, line)
	}

	if !(err == nil || err == io.EOF) {
		l.process.Log(fmt.Sprintf("%v", err))
	}
}

type loggingWriter struct {
	logger
	writer io.Writer
}

func (l *loggingWriter) Write(bytes []byte) (int, error) {
	if l.writer == nil {
		l.LogLines(" <- ", string(bytes), nil)
		return len(bytes), nil
	}
	n, err := l.writer.Write(bytes)
	l.LogLines(" <- ", string(bytes[:n]), err)
	return n, err
}

type loggingReader struct {
	logger
	reader io.Reader
}

func (l *loggingReader) Read(p []byte) (n int, err error) {
	n, err = l.reader.Read(p)
	l.LogLines(" -> ", string(p[:n]), err)
	return n, err
}

// A Cmd is like an os/exec.Cmd, but logs what happens on
// stdin/stdout/stderr, and has a slightly different API.
type Cmd struct {
	*exec.Cmd
	supervisorProcess *Process
}

func (c *Cmd) pre() {
	if c.Stdin != nil {
		c.Stdin = &loggingReader{logger: logger{process: c.supervisorProcess}, reader: c.Stdin}
	}
	c.Stdout = &loggingWriter{logger: logger{process: c.supervisorProcess}, writer: c.Stdout}
	c.Stderr = &loggingWriter{logger: logger{process: c.supervisorProcess}, writer: c.Stderr}

	c.supervisorProcess.Logf("%s %v", c.Path, c.Args[1:])
}

func (c *Cmd) post(err error) {
	if err == nil {
		c.supervisorProcess.Logf("%s exited successfully", c.Path)
	} else {
		if c.ProcessState == nil {
			c.supervisorProcess.Logf("%v", err)
		} else {
			c.supervisorProcess.Logf("%s: %v", c.Path, err)
		}
	}
}

// Start is like os/exec.Cmd.Start.
func (c *Cmd) Start() error {
	c.pre()
	return c.Cmd.Start()
}

// Wait is like os/exec.Cmd.Wait.
func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	c.post(err)
	return err
}

// Run is like os/exec.Cmd.Run.
func (c *Cmd) Run() error {
	c.pre()
	err := c.Cmd.Run()
	c.post(err)
	return err
}

// Command creates a single purpose supervisor and uses it to produce
// and return a *supervisor.Cmd.
func Command(prefix, name string, args ...string) (result *Cmd) {
	MustRun(prefix, func(p *Process) error {
		result = p.Command(name, args...)
		return nil
	})
	return
}

// Command creates a command that automatically logs inputs, outputs,
// and exit codes to the process logger.
func (p *Process) Command(name string, args ...string) *Cmd {
	return &Cmd{exec.Command(name, args...), p}
}

// Capture runs a command with the supplied input and captures the
// output as a string.
func (c *Cmd) Capture(stdin io.Reader) (output string, err error) {
	c.Stdin = stdin
	out := strings.Builder{}
	c.Stdout = &out
	err = c.Run()
	output = out.String()
	return
}

// MustCapture is like Capture, but panics if there is an error.
func (c *Cmd) MustCapture(stdin io.Reader) (output string) {
	output, err := c.Capture(stdin)
	if err != nil {
		panic(err)
	}
	return output
}

// CaptureErr runs a command with the supplied input and captures
// stdout and stderr as a string.
func (c *Cmd) CaptureErr(stdin io.Reader) (output string, err error) {
	c.Stdin = stdin
	out := strings.Builder{}
	c.Stdout = &out
	c.Stderr = &out
	err = c.Run()
	output = out.String()
	return
}

// MustCaptureErr is like CaptureErr, but panics if there is an error.
func (c *Cmd) MustCaptureErr(stdin io.Reader) (output string) {
	output, err := c.CaptureErr(stdin)
	if err != nil {
		panic(err)
	}
	return output
}
