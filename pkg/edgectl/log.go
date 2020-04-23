package edgectl

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// DaemonFormatter formats log messages for the Edge Control Daemon
type DaemonFormatter struct {
	TimestampFormat string
}

// Format implement logrus.Formatter
func (f *DaemonFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	fmt.Fprintf(b, "%s %s", entry.Time.Format(f.TimestampFormat), entry.Message)

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

// SetUpLogging sets up standard Edge Control Daemon logging
func SetUpLogging() supervisor.Logger {
	loggingToTerminal := terminal.IsTerminal(int(os.Stdout.Fd()))
	logger := logrus.StandardLogger()
	formatter := new(DaemonFormatter)
	logger.Formatter = formatter
	if loggingToTerminal {
		formatter.TimestampFormat = "15:04:05"
	} else {
		formatter.TimestampFormat = "2006/01/02 15:04:05"
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
