package dexec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

func fixupWriter(o io.Writer, log func(string)) io.Writer {
	if o == nil {
		o = nilWriter{}
	}
	if _, isFile := o.(*os.File); isFile {
		return o
	}
	o = &loggingWriter{
		log:    log,
		writer: o,
	}
	return o
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

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
		if utf8.Valid(line) {
			if utf8.RuneCount(line) > 80 {
				truncated := line
				for utf8.RuneCount(truncated) > 80 {
					_, size := utf8.DecodeLastRune(truncated)
					truncated = truncated[:len(truncated)-size]
				}
				l.log(fmt.Sprintf("%q… (%d runes truncated)",
					truncated,
					utf8.RuneCount(line)-utf8.RuneCount(truncated)))
			} else {
				l.log(fmt.Sprintf("%q", line))
			}
		} else {
			l.log(fmt.Sprintf("[...%d bytes of binary data...]", len(line)))
		}
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
