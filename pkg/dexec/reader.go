package dexec

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

func fixupReader(o io.Reader, log func(string)) io.Reader {
	if o == nil {
		o = nilReader{}
	}
	if _, isFile := o.(*os.File); isFile {
		return o
	}
	o = &loggingReader{
		log:    log,
		reader: o,
	}
	return o
}

type nilReader struct{}

func (nilReader) Read(_ []byte) (int, error) { return 0, io.EOF }

type loggingReader struct {
	log    func(string)
	reader io.Reader
}

func (l *loggingReader) Read(p []byte) (n int, err error) {
	n, err = l.reader.Read(p)

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
