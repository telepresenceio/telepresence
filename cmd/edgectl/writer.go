package main

import (
	"bytes"
	"io"
	"log"
)

// NewLoggingWriter returns an io.Writer that logs each write line-by-line
// blindly passing through non-UTF-8 and over-long lines.
func NewLoggingWriter(l *log.Logger) io.Writer {
	return &loggingWriter{l}
}

type loggingWriter struct {
	log *log.Logger
}

func (l *loggingWriter) Write(toLog []byte) (int, error) {
	n := len(toLog)
	for len(toLog) > 0 {
		nl := bytes.IndexByte(toLog, '\n')
		var line []byte
		if nl < 0 {
			line = toLog
			toLog = nil
		} else {
			line = toLog[:nl+1]
			toLog = toLog[nl+1:]
		}
		l.log.Print(string(line))
	}
	return n, nil
}
