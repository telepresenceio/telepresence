package logging

import (
	"context"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/datawire/dlib/dlog"
)

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = terminal.IsTerminal

// InitContext sets up standard Telepresence logging for a background process
func InitContext(ctx context.Context, name string) (context.Context, error) {
	logger := logrus.StandardLogger()
	logger.SetLevel(logrus.DebugLevel)

	if IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = NewFormatter("15:04:05")
	} else {
		logger.Formatter = NewFormatter("2006/01/02 15:04:05")
		logger.SetOutput(&lumberjack.Logger{
			Filename:   filepath.Join(Dir(), name+".log"),
			MaxSize:    10,   // megabytes
			MaxBackups: 3,    // in the same directory
			MaxAge:     60,   // days
			LocalTime:  true, // rotated logfiles use local time names
		})
	}
	return dlog.WithLogger(ctx, dlog.WrapLogrus(logger)), nil
}
