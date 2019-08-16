package supervisor

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestMustCapture(t *testing.T) {
	MustRun("bob", func(p *Process) error {
		result := p.Command("echo", "this", "is", "a", "test").MustCapture(nil)
		if result != "this is a test\n" {
			t.Errorf("unexpected result: %v", result)
		}
		return nil
	})
}

func TestCaptureError(t *testing.T) {
	MustRun("bob", func(p *Process) error {
		_, err := p.Command("nosuchcommand").Capture(nil)
		if err == nil {
			t.Errorf("expected an error")
		}
		return nil
	})
}

func TestCaptureExitError(t *testing.T) {
	MustRun("bob", func(p *Process) error {
		_, err := p.Command("test", "1", "==", "0").Capture(nil)
		if err == nil {
			t.Errorf("expected an error")
		}
		return nil
	})
}

func TestCaptureInput(t *testing.T) {
	MustRun("bob", func(p *Process) error {
		output, err := p.Command("cat").Capture(strings.NewReader("hello"))
		if err != nil {
			t.Errorf("unexpected error")
		}
		if output != "hello" {
			t.Errorf("expected hello, got %v", output)
		}
		return nil
	})
}

func TestCommandRun(t *testing.T) {
	MustRun("bob", func(p *Process) error {
		err := p.Command("ls").Run()
		if err != nil {
			t.Errorf("unexpted error: %v", err)
		}
		return nil
	})
}

type LogToSlice struct {
	Lines []string
}

func (lb *LogToSlice) Printf(format string, v ...interface{}) {
	lb.Lines = append(lb.Lines, fmt.Sprintf(format, v...))
}

func TestCommandRunLogging(t *testing.T) {
	sup := WithContext(context.Background())
	theLogger := &LogToSlice{}
	sup.Logger = theLogger
	sup.Supervise(&Worker{
		Name: "charles",
		Work: func(p *Process) error {
			cmd := p.Command("bash", "-c", "for i in $(seq 1 3); do echo $i; sleep 0.2; done")
			if err := cmd.Run(); err != nil {
				t.Errorf("unexpted error: %v", err)
			}
			if len(theLogger.Lines) != 6 {
				t.Log("Expected 6 lines: process start, cmd start, 1, 2, 3, cmd end")
				t.Logf("Got (%d lines): %q", len(theLogger.Lines), theLogger.Lines)
				t.Fail()
			}
			return nil
		},
	})
	sup.Run()
}
