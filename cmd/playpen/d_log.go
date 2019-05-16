package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/natefinch/lumberjack.v2"
)

// DaemonFormatter formats log messages for the Playpen Daemon
type DaemonFormatter struct{}

// Format implement logrus.Formatter
func (f *DaemonFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	fmt.Fprintf(b, "%s %s", entry.Time.Format("2006/01/02 15:04:05"), entry.Message)

	if len(entry.Data) > 0 {
		keys := make([]string, 0, len(entry.Data))
		for k := range entry.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := entry.Data[k]
			fmt.Fprintf(b, " %s=%+v", k, v)
		}
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// SetUpLogging sets up standard Playpen Daemon logging
func SetUpLogging() supervisor.Logger {
	logger := logrus.StandardLogger()
	logger.Formatter = new(DaemonFormatter)
	if !terminal.IsTerminal(int(os.Stdout.Fd())) {
		logger.SetOutput(&lumberjack.Logger{
			Filename:   logfile,
			MaxSize:    10,   // megabytes
			MaxBackups: 3,    // in the same directory
			MaxAge:     60,   // days
			LocalTime:  true, // rotated logfiles use local time names
		})
	}
	return logger
}

func doWordWrap(text string, prefix string, lineWidth int) []string {
	words := strings.Fields(strings.TrimSpace(text))
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0)
	wrapped := prefix + words[0]
	for _, word := range words[1:] {
		if len(word)+1 > lineWidth-len(wrapped) {
			lines = append(lines, wrapped)
			wrapped = prefix + word
		} else {
			wrapped += " " + word
		}
	}
	if len(wrapped) > 0 {
		lines = append(lines, wrapped)
	}
	return lines
}

var terminalWidth = 0 // Set on first use

// WordWrap returns a slice of strings with the original content wrapped at the
// terminal width or at 80 characters if no terminal is present.
func WordWrap(text string) []string {
	if terminalWidth <= 0 {
		terminalWidth = 80
		fd := int(os.Stdout.Fd())
		if terminal.IsTerminal(fd) {
			w, _, err := terminal.GetSize(fd)
			if err == nil {
				terminalWidth = w
			}
		}
	}
	return doWordWrap(text, "", terminalWidth)
}

// WordWrapString returns a string with the original content wrapped at the
// terminal width or at 80 characters if no terminal is present.
func WordWrapString(text string) string {
	return strings.Join(WordWrap(text), "\n")
}
