package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type Logger struct {
	file *os.File
	// writer  *bufio.Writer
	outCh   chan string
	doneCh  chan struct{}
	outputs map[TestID]string
}

func NewLogger(ctx context.Context, path string) (*Logger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	l := &Logger{
		file: f,
		// writer:  bufio.NewWriter(f),
		outCh:   make(chan string, 1024),
		doneCh:  make(chan struct{}),
		outputs: make(map[TestID]string),
	}
	go l.run(ctx)
	return l, nil
}

func (l *Logger) run(ctx context.Context) {
	defer close(l.doneCh)
	for {
		select {
		case <-ctx.Done():
			return
		case out, ok := <-l.outCh:
			if !ok {
				return
			}
			l.file.WriteString(out)
		}
	}
}

func (l *Logger) CloseAndWait() {
	close(l.outCh)
	// l.writer.Flush()
	l.file.Close()
	<-l.doneCh
}

func (l *Logger) Report(line *Line) {
	switch line.Action {
	case RUN:
		// There's an OUTPUT line that reports the RUN and the failures and stuff, we just really need formatting here.
		l.outputs[line.TestID] = "\n"
		parts := strings.Split(line.TestID.Test, "/")
		for i := 0; i < len(parts)-1; i++ {
			// If this is a subtest of a test, then we need to dump the output of the parent test so far.
			parent := TestID{
				Package: line.TestID.Package,
				Test:    strings.Join(parts[:i+1], "/"),
			}
			if output, ok := l.outputs[parent]; ok {
				l.outCh <- output
				l.outputs[parent] = ""
			}
		}
	case PASS, FAIL, SKIP:
		l.outputs[line.TestID] += "\n"
		l.outCh <- l.outputs[line.TestID]
		if line.Action != FAIL {
			delete(l.outputs, line.TestID)
		}
	case OUTPUT:
		l.outputs[line.TestID] += line.Output
	}
}

func (l *Logger) ReportFailures() bool {
	if len(l.outputs) == 0 {
		return false
	}
	separator := strings.Repeat("=", 200)
	fmt.Fprintf(os.Stderr, "\n\nFailed tests:\n")
	for testID, output := range l.outputs {
		fmt.Fprintf(os.Stderr, "\n%s.%s\n%s\n%s\n", testID.Package, testID.Test, output, separator)
	}
	return true
}
