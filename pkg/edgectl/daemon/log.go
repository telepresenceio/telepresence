package daemon

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// formatter formats log messages for the Edge Control Daemon
type formatter struct {
	timestampFormat string
}

// Format implement logrus.Formatter
func (f *formatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	fmt.Fprintf(b, "%s %s", entry.Time.Format(f.timestampFormat), entry.Message)

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

// setUpLogging sets up standard Edge Control Daemon logging
func setUpLogging() supervisor.Logger {
	loggingToTerminal := terminal.IsTerminal(int(os.Stdout.Fd()))
	logger := logrus.StandardLogger()
	formatter := new(formatter)
	logger.Formatter = formatter
	if loggingToTerminal {
		formatter.timestampFormat = "15:04:05"
	} else {
		formatter.timestampFormat = "2006/01/02 15:04:05"
		logger.SetOutput(&lumberjack.Logger{
			Filename:   edgectl.Logfile,
			MaxSize:    10,   // megabytes
			MaxBackups: 3,    // in the same directory
			MaxAge:     60,   // days
			LocalTime:  true, // rotated logfiles use local time names
		})
	}
	return logger
}
