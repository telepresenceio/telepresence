package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
)

// NewLoggingWriter returns an io.Writer that passes through Write()s to the
// underlying Writer while logging each write line-by-line. Unlike dexec's
// logger, this one assumes the data is fine to log, blindly passing through
// non-UTF-8 and over-long lines.
func NewLoggingWriter(l *log.Logger, w io.Writer) io.Writer {
	return &loggingWriter{
		log:    func(s string) { l.Print(s) },
		writer: w,
	}
}

type loggingWriter struct {
	log    func(string)
	writer io.Writer
}

func (l *loggingWriter) Write(p []byte) (n int, err error) {
	n, err = l.writer.Write(p)

	toLog := p[:n]
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
		l.log(string(line))
	}

	if err != nil {
		if err == io.EOF {
			l.log("EOF")
		} else {
			l.log(fmt.Sprintf("error = %v", err))
		}
	}

	return n, err
}
